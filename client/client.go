package main

import (
	"bufio"
	"context"
	"flag"
	"google.golang.org/grpc/credentials/insecure"
	"io"
	"log"
	"os"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"grpc-chat/data"
	pb "grpc-chat/protos"
)

var (
	userName                = flag.String("user", "Tommy", "Provide user name")
	tls                     = flag.Bool("tls", false, "Connection uses TLS if true, else plain TCP")
	caFile                  = flag.String("ca_file", "", "The file containing the CA root cert file")
	serverAddr              = flag.String("addr", "localhost:50051", "The server address in the format of host:port")
	serverHostOverride      = flag.String("server_host_override", "x.test.example.com", "The server name used to verify the hostname returned by the TLS handshake")
	pullMessagesIntervalSec = flag.Int64("pull_msgs_interval", 1, "Interval of pulling messages from a server")
)

type chatClient struct {
	grpcClient             pb.GrpcChatClient
	user                   *pb.User
	rooms                  map[uint32]*pb.Room
	lastPulledMessageIndex map[uint32]uint32 // per room by map key
	pullTimer              *time.Timer
}

func (c *chatClient) GetRooms() {
	log.Printf("Get rooms from server")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	rooms, err := c.grpcClient.GetRooms(ctx, &pb.Empty{})
	if err != nil {
		log.Fatalf("%v.GetRooms(_) = _, %v: ", c.grpcClient, err)
	}

	for _, room := range rooms.GetRooms() {
		c.rooms[room.GetId().GetId()] = room
	}
}

func (c *chatClient) getRoomByName(name string) *pb.Room {
	for _, room := range c.rooms {
		if room.GetName() == name {
			return room
		}
	}
	return nil
}

// CreateJoinRoom adds new room or joins to existing one.
func (c *chatClient) CreateJoinRoom(roomName string) (uint32, error) {
	c.GetRooms()

	if room := c.getRoomByName(roomName); room != nil {
		log.Printf("Room (%s) already exists.", room.GetName())
		return c.joinRoom(room)
	}

	return c.createRoom(roomName)
}

func (c *chatClient) createRoom(roomName string) (uint32, error) {
	log.Printf("Create new room (%s)", roomName)
	roomDefinition := &pb.UserRoomByName{
		Name: roomName,
		User: c.user,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	createdRoom, err := c.grpcClient.CreateRoom(ctx, roomDefinition)
	if err != nil {
		log.Fatalf("%v.CreateRoom(_) = _, %v: ", c.grpcClient, err)
		return 0, err
	}
	log.Printf("Room (%s) has been created", createdRoom.GetName())
	c.rooms[createdRoom.GetId().GetId()] = createdRoom
	return createdRoom.GetId().GetId(), nil
}

func (c *chatClient) joinRoom(room *pb.Room) (uint32, error) {
	log.Printf("Join room (%s)", room.GetName())
	joinDefinition := &pb.UserRoomById{
		Id:   room.GetId().GetId(),
		User: c.user,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	joinedRoom, err := c.grpcClient.JoinRoom(ctx, joinDefinition)
	if err != nil {
		log.Fatalf("%v.JoinRoom(_) = _, %v: ", c.grpcClient, err)
		return 0, err
	}
	log.Printf("Joined to room (%s)", joinedRoom.GetName())
	log.Println(joinedRoom)
	return joinedRoom.GetId().GetId(), nil
}

func (c *chatClient) LeaveRoom(roomName string) {
	log.Printf("Leave room (%s)", roomName)
	if room := c.getRoomByName(roomName); room != nil {
		roomById := &pb.UserRoomById{
			Id:   room.GetId().GetId(),
			User: c.user,
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		leavedRoom, err := c.grpcClient.LeaveRoom(ctx, roomById)
		if err != nil {
			log.Fatalf("%v.LeaveRoom(_) = _, %v: ", c.grpcClient, err)
		}
		room.Users = leavedRoom.Users
	}
}

func (c *chatClient) SendMessage(roomId uint32, message string) {
	log.Printf("Send message")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	chatMessage := &pb.ChatMessage{
		User: c.user,
		Id: &pb.RoomMessageId{
			RoomId: &pb.RoomId{Id: roomId},
		},
		Text: message,
	}

	_, err := c.grpcClient.SendMessage(ctx, chatMessage)
	if err != nil {
		log.Fatalf("%v.SendMessage(_) = _, %v: ", c.grpcClient, err)
	}
}

func (c *chatClient) PrintMessages(roomId uint32) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	lastPulledMessage := c.lastPulledMessageIndex[roomId]

	stream, err := c.grpcClient.GetMessages(ctx, &pb.RoomMessageId{
		RoomId:    &pb.RoomId{Id: roomId},
		MessageId: &pb.MessageId{Id: lastPulledMessage + 1},
	})

	if err != nil {
		log.Fatalf("%v.GetMessages(_) = _, %v", c.grpcClient, err)
	}

	for {
		message, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			log.Fatalf("%v.GetMessages(_) = _, %v", c.grpcClient, err)
		}
		log.Printf("\n============================================\n"+
			"From: %s\n%s\n", message.GetUser().GetName(), message.GetText())
		c.lastPulledMessageIndex[roomId] = message.GetId().GetMessageId().GetId()
	}

	// Restart timer
	timeout := time.Duration(*pullMessagesIntervalSec) * time.Second
	c.pullTimer = time.AfterFunc(timeout, func() {
		c.PrintMessages(roomId)
	})
}

func GrpcConnect() (*grpc.ClientConn, error) {
	var opts []grpc.DialOption
	if *tls {
		if *caFile == "" {
			*caFile = data.Path("x509/ca_cert.pem")
		}
		userCredentials, err := credentials.NewClientTLSFromFile(*caFile, *serverHostOverride)
		if err != nil {
			log.Fatalf("Failed to create TLS credentials %v", err)
			return nil, err
		}
		opts = append(opts, grpc.WithTransportCredentials(userCredentials))
	} else {
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}

	conn, err := grpc.Dial(*serverAddr, opts...)
	if err != nil {
		log.Fatalf("fail to dial: %v", err)
		return nil, err
	}
	return conn, nil
}

func NewChatClient(conn grpc.ClientConnInterface) *chatClient {
	grpcClient := pb.NewGrpcChatClient(conn)
	client := &chatClient{
		grpcClient: grpcClient,
		user: &pb.User{
			Name: *userName,
			Id:   0,
		},
		rooms:                  make(map[uint32]*pb.Room),
		lastPulledMessageIndex: make(map[uint32]uint32),
	}
	return client
}

func main() {
	flag.Parse()

	conn, err := GrpcConnect()
	if err != nil {
		log.Fatalf("fail to dial: %v", err)
		return
	}

	defer func(conn *grpc.ClientConn) {
		err := conn.Close()
		if err != nil {
			log.Fatalf("fail to close grpc connection: %v", err)
		}
	}(conn)

	client := NewChatClient(conn)

	// Create new room
	roomId, err := client.CreateJoinRoom("main")
	if err != nil {
		log.Fatalf("%v.CreateJoinRoom(_) = _, %v: ", client.grpcClient, err)
		return
	}

	// Schedule messages pull
	timeout := time.Duration(*pullMessagesIntervalSec) * time.Second
	client.pullTimer = time.AfterFunc(timeout, func() {
		client.PrintMessages(roomId)
	})

	// Read from stdin to send messages
	reader := bufio.NewReader(os.Stdin)
	for {
		//fmt.Print("Enter text: ")
		text, _ := reader.ReadString('\n')
		text = text[:len(text)-1]
		if text == "exit" {
			break
		}
		client.SendMessage(roomId, text)
	}

	client.LeaveRoom("main")
}
