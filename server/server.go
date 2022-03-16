package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"sync"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"grpc-chat/data"
	pb "grpc-chat/protos"
)

var (
	tls                = flag.Bool("tls", false, "Connection uses TLS if true, else plain TCP")
	certFile           = flag.String("cert_file", "", "The TLS cert file")
	keyFile            = flag.String("key_file", "", "The TLS key file")
	port               = flag.Int("port", 50051, "The server port")
	maxPages           = flag.Int("max_pages", 10, "Maximum amount of pages")
	maxMessagesPerPage = flag.Int("max_page_msgs", 100, "Maximum amount of messages per page")
)

type chatServer struct {
	pb.UnimplementedGrpcChatServer
	users map[uint32]*pb.User
	rooms map[uint32]*pb.Room
	mu    sync.Mutex // protects routeNotes
}

// CreateRoom adds new room on server.
func (s *chatServer) CreateRoom(_ context.Context, newRoomDefinition *pb.UserRoomByName) (*pb.Room, error) {
	newUuid := uuid.New()
	if _, exists := s.rooms[newUuid.ID()]; !exists {
		newRoom := &pb.Room{
			Name:          newRoomDefinition.GetName(),
			Id:            &pb.RoomId{Id: newUuid.ID()},
			Users:         make(map[uint32]*pb.User),
			PagedMessages: make(map[uint32]*pb.ChatMessages),
			PagesIndexes:  make([]uint32, 1),
			LastMessageId: &pb.MessageId{Id: 0},
		}
		newRoom.PagesIndexes[0] = 0
		newRoom.PagedMessages[0] = &pb.ChatMessages{Messages: make([]*pb.ChatMessage, 0)}
		newRoom.Users[newRoomDefinition.GetUser().GetId()] = newRoomDefinition.GetUser()
		s.rooms[newRoom.GetId().GetId()] = newRoom
		return newRoom, nil
	}
	return nil, errors.New(fmt.Sprintf("Room %d already exists", newUuid.ID()))
}

// JoinRoom joins user to a room
func (s *chatServer) JoinRoom(_ context.Context, roomDefinition *pb.UserRoomById) (*pb.Room, error) {
	if room, exists := s.rooms[roomDefinition.GetId()]; exists {
		room.Users[roomDefinition.GetUser().GetId()] = roomDefinition.GetUser()
		return room, nil
	}
	return nil, errors.New(fmt.Sprintf("Room with id %d does not exists", roomDefinition.GetId()))
}

// LeaveRoom joins user to a room
func (s *chatServer) LeaveRoom(_ context.Context, roomDefinition *pb.UserRoomById) (*pb.Room, error) {
	if room, exists := s.rooms[roomDefinition.GetId()]; exists {
		delete(room.Users, roomDefinition.GetId())
		return room, nil
	}
	return nil, errors.New(fmt.Sprintf("Room with id %d does not exists", roomDefinition.GetId()))
}

//GetRooms get rooms existing on the server.
func (s *chatServer) GetRooms(_ context.Context, _ *pb.Empty) (*pb.Rooms, error) {
	rooms := &pb.Rooms{Rooms: make([]*pb.Room, 0)}
	for _, room := range s.rooms {
		rooms.Rooms = append(rooms.Rooms, room)
	}
	return rooms, nil
}

func (s *chatServer) SendMessage(_ context.Context, message *pb.ChatMessage) (*pb.Empty, error) {
	if room, exists := s.rooms[message.GetId().GetRoomId().GetId()]; exists {
		s.mu.Lock()
		lastPageId := room.GetPagesIndexes()[len(room.GetPagesIndexes())-1]

		if len(room.GetPagedMessages()[lastPageId].GetMessages()) >= *maxMessagesPerPage {
			log.Println("Max messages in current page. Add additional page.")
			lastPageId += 1
			room.PagesIndexes = append(room.GetPagesIndexes(), lastPageId)
			room.GetPagedMessages()[lastPageId] = &pb.ChatMessages{Messages: make([]*pb.ChatMessage, 0)}

			if numOfPages := len(room.GetPagedMessages()); numOfPages > *maxPages {
				log.Printf("Max pages reached (%d). Delete first page.", numOfPages)
				log.Println(room.GetPagesIndexes())
				delete(room.PagedMessages, room.GetPagesIndexes()[0])
				x, a := room.GetPagesIndexes()[0], room.GetPagesIndexes()[1:]
				x, a = a[len(a)-1], a[:len(a)-1]
				room.PagesIndexes = append(a, x)
				log.Println(room.GetPagesIndexes())
			}
		}

		messageId := room.GetLastMessageId().GetId() + 1
		message.GetId().MessageId = &pb.MessageId{Id: messageId}

		room.GetPagedMessages()[lastPageId].Messages = append(room.GetPagedMessages()[lastPageId].GetMessages(), message)
		room.GetLastMessageId().Id = messageId
		s.mu.Unlock()
	}
	return &pb.Empty{}, nil
}

func (s *chatServer) GetMessages(fromId *pb.RoomMessageId, stream pb.GrpcChat_GetMessagesServer) error {
	if room, exists := s.rooms[fromId.GetRoomId().GetId()]; exists {
		for _, pageIndex := range room.GetPagesIndexes() {
			log.Printf("GetMessages: room id (%d)", pageIndex)
			for _, message := range room.GetPagedMessages()[pageIndex].GetMessages() {
				if message.GetId().GetMessageId().GetId() < fromId.GetMessageId().GetId() {
					continue
				}
				log.Printf("GetMessages: message id (%d)", message.GetId().GetMessageId().GetId())
				if err := stream.Send(message); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func newServer() *chatServer {
	s := &chatServer{
		rooms: make(map[uint32]*pb.Room),
	}
	return s
}

func main() {
	flag.Parse()
	lis, err := net.Listen("tcp", fmt.Sprintf("localhost:%d", *port))
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}
	var opts []grpc.ServerOption
	if *tls {
		if *certFile == "" {
			*certFile = data.Path("x509/server_cert.pem")
		}
		if *keyFile == "" {
			*keyFile = data.Path("x509/server_key.pem")
		}
		creds, err := credentials.NewServerTLSFromFile(*certFile, *keyFile)
		if err != nil {
			log.Fatalf("Failed to generate credentials %v", err)
		}
		opts = []grpc.ServerOption{grpc.Creds(creds)}
	}
	grpcServer := grpc.NewServer(opts...)
	pb.RegisterGrpcChatServer(grpcServer, newServer())
	log.Println("Starting server")
	err = grpcServer.Serve(lis)
	if err != nil {
		log.Fatalf("Failed to serve %v", err)
	}
}
