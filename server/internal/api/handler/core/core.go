package handler

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"

	"chat-application/internal/api/model"
	roomRepository "chat-application/internal/repo/room"
	websoc "chat-application/internal/websocket"
	"chat-application/util"

	"github.com/google/uuid"
)

type CoreHandler struct {
	core *websoc.Core
	roomRepository *roomRepository.RoomRepository
	roomLimit int
}

func NewCoreHandler(c *websoc.Core) *CoreHandler  {
	roomLimit := 50
	if maxRooomStr := util.GetEnv("MAX_ROOMS", ""); maxRooomStr != "" {
		if limit, err := strconv.Atoi(maxRooomStr); err == nil {
			roomLimit = limit
		}
	}

	return  &CoreHandler{
		core: c,
		roomRepository: roomRepository.NewRoomRepository(c.GetDB()),
		roomLimit: roomLimit,
	}
}

func (h *CoreHandler) CreateRoom(w http.ResponseWriter, r *http.Request) {
	var req model.CreateRoomReq

	log.Printf("CreateRoom request: %s", r.URL.Path)

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		util.WriteErrorResponse(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	ctx := r.Context()
	
	var creatorID *uuid.UUID
	if userIDStr, ok := ctx.Value("userID").(string); ok {
		log.Printf("User ID from context: %s", userIDStr)
		if uid, err := uuid.Parse(userIDStr); err == nil {
			creatorID = &uid

			hasRoom, err := h.roomRepository.HasActiveRoom(ctx, uid)
			if err != nil {
				util.WriteErrorResponse(w, http.StatusInternalServerError, "Failed to check active rooms")
				return
			}

			if hasRoom {
				util.WriteErrorResponse(w, http.StatusForbidden, "User already has an active room")
				return
			}
			
		} else {
			log.Printf("Failed to parse user ID: %v", err)
		}
	} else {
		log.Printf("User ID not found in context")
	}

	activeRooms, err := h.roomRepository.CountActiveRooms(ctx)
	if err != nil {
		util.WriteErrorResponse(w, http.StatusInternalServerError, "Failed to retrieve active rooms")
		return
	}

	log.Printf("Active rooms: %d, limit: %d", activeRooms, h.roomLimit)

	if activeRooms >= h.roomLimit {
		util.WriteErrorResponse(w, http.StatusTooManyRequests, "Room limit reached")
		return
	}

	room := &roomRepository.Room{
		Name: req.Name,
		CreatorID: creatorID,
	}

	room, err = h.roomRepository.CreateRoom(ctx, room)
	if err != nil {
		util.WriteErrorResponse(w, http.StatusInternalServerError, "Failed to create room")
		return
	}

	h.core.Rooms[room.ID.String()] = &websoc.Room{
		ID: room.ID.String(),
		Name: room.Name,
		Clients: make(map[string]*websoc.Client),
		IsPinned: room.IsPinned,
		TopicTitle: room.TopicTitle,
		TopicDescription: room.TopicDescription,
		TopicURL: room.TopicURL,
		TopicSource: room.TopicSource,
	}

	response := model.CreateRoomReq{
		ID: room.ID.String(),
		Name: room.Name,
	}
	
	util.WriteJSONResponse(w, http.StatusOK, response)

}

func (h *CoreHandler) JoinRoom(w http.ResponseWriter, r *http.Request) {
	roomID := r.URL.Query().Get("room_id")
	if roomID == "" {
		util.WriteErrorResponse(w, http.StatusBadRequest, "Room ID is required")
		return
	}

	roomUUID, err := uuid.Parse(roomID)
	if err != nil {
		util.WriteErrorResponse(w, http.StatusBadRequest, "Invalid Room ID format")
		return
	}

	ctx := r.Context()
	dbRoom, err := h.roomRepository.GetRoomByID(ctx, roomUUID)
	if err != nil {
		util.WriteErrorResponse(w, http.StatusInternalServerError, "Failed to retrieve room")
		return
	}

	if dbRoom == nil {
		util.WriteErrorResponse(w, http.StatusNotFound, "Room not found")
		return
	}

	if _, exists := h.core.Rooms[dbRoom.ID.String()]; !exists {
		h.core.Rooms[roomID] = &websoc.Room{
			ID: roomID,
			Name: dbRoom.Name,
			Clients: make(map[string]*websoc.Client),
			IsPinned: dbRoom.IsPinned,
			TopicTitle: dbRoom.TopicTitle,
			TopicDescription: dbRoom.TopicDescription,
			TopicURL: dbRoom.TopicURL,
			TopicSource: dbRoom.TopicSource,
		}
	}

	var upgrader = websocket.Upgrader{
		ReadBufferSize: 1024,
		WriteBufferSize: 1024,
		CheckOrigin: func(r *http.Request) bool  {
			return true
		},

		EnableCompression: true,
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		util.WriteErrorResponse(w, http.StatusInternalServerError, "Failed to upgrade connection")
		return
	}

	q := r.URL.Query()
	clientID := q.Get("client_id")
	username := q.Get("username")

	cl := &websoc.Client{
		Conn: conn,
		Message: make(chan *websoc.Message),
		ID: clientID,
		RoomID: roomID,
		Username: username,
	}

	h.core.Register <- cl

	go cl.WriteMessage()
	cl.ReadMessage(h.core)
}

func (h *CoreHandler) GetRooms(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Fetch active rooms from database
	dbRooms, err := h.roomRepository.GetAllActiveRooms(ctx)
	if err != nil {
		util.WriteErrorResponse(w, http.StatusInternalServerError, "failed to fetch rooms")
		return
	}

	rooms := make([]model.RoomRes, 0, len(dbRooms))
	for _, room := range dbRooms {
		rooms = append(rooms, model.RoomRes{
			ID:               room.ID.String(),
			Name:             room.Name,
			IsPinned:         room.IsPinned,
			CreatedAt:        room.CreatedAt,
			Expires:          room.ExpiresAt,
			TopicTitle:       room.TopicTitle,
			TopicDescription: room.TopicDescription,
			TopicURL:         room.TopicURL,
			TopicSource:      room.TopicSource,
		})

		// Ensure room exists in memory map
		if _, exists := h.core.Rooms[room.ID.String()]; !exists {
			h.core.Rooms[room.ID.String()] = &websoc.Room{
				ID:               room.ID.String(),
				Name:             room.Name,
				Clients:          make(map[string]*websoc.Client),
				IsPinned:         room.IsPinned,
				TopicTitle:       room.TopicTitle,
				TopicDescription: room.TopicDescription,
				TopicURL:         room.TopicURL,
				TopicSource:      room.TopicSource,
			}
		}
	}

	util.WriteJSONResponse(w, http.StatusOK, rooms)
}

func (h *CoreHandler) GetClients(w http.ResponseWriter, r *http.Request) {
	var clients []model.ClientRes
	roomID := chi.URLParam(r, "room_id")

	if _, ok := h.core.Rooms[roomID]; !ok {
		util.WriteErrorResponse(w, http.StatusNotFound, "Room not found")
		return
	}

	for _, c := range h.core.Rooms[roomID].Clients {
		clients = append(clients, model.ClientRes{
			ID: c.ID,
			Username: c.Username,
		})
	}

	util.WriteJSONResponse(w, http.StatusOK, clients)
}