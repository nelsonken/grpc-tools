package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"github.com/bradleyjkemp/grpc-tools/grpc-proxy"
	"github.com/bradleyjkemp/grpc-tools/internal"
	"github.com/bradleyjkemp/grpc-tools/internal/tls"
	"golang.org/x/net/context"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"io"
	"os"
	"time"
)

var (
	destinationOverride = flag.String("destination", "", "Destination server to forward requests to. By default the destination for each RPC is autodetected from the dump metadata.")
	dumpPath            = flag.String("dump", "", "The gRPC dump to replay requests from")
)

func main() {
	flag.Parse()
	err := run()
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		flag.Usage()
		os.Exit(1)
	}
}

func run() error {
	dumpFile, err := os.Open(*dumpPath)
	if err != nil {
		return err
	}

	dumpDecoder := json.NewDecoder(dumpFile)
RPC:
	for {
		rpc := internal.RPC{}
		err := dumpDecoder.Decode(&rpc)
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("failed to decode dump: %s", err)
		}

		conn, err := getConnection(rpc.Metadata)
		if err != nil {
			return fmt.Errorf("failed to connect to destination (%s): %s", *destinationOverride, err)
		}
		ctx := metadata.NewOutgoingContext(context.Background(), rpc.Metadata)
		streamName := rpc.StreamName()
		str, err := conn.NewStream(ctx, &grpc.StreamDesc{
			StreamName:    streamName,
			ServerStreams: true,
			ClientStreams: true,
		}, streamName)
		if err != nil {
			return fmt.Errorf("failed to make new stream: %v", err)
		}

		fmt.Print(streamName, "...")
		for _, message := range rpc.Messages {
			switch message.MessageOrigin {
			case internal.ClientMessage:
				err := str.SendMsg(message.RawMessage)
				if err != nil {
					return fmt.Errorf("failed to send message: %v", err)
				}
			case internal.ServerMessage:
				var resp []byte
				err := str.RecvMsg(&resp)
				if err != nil {
					// TODO when do we assert on RPC errors?
					return fmt.Errorf("failed to recv message: %v", err)
				}
				if string(resp) != string(message.RawMessage) {
					fmt.Println("Err mismatch")
					continue RPC
				}
			default:
				return fmt.Errorf("invalid message type: %v", message.MessageOrigin)
			}
		}
		fmt.Println("OK")
	}
	return nil
}

var cachedConns = internal.NewConnPool()

func getConnection(md metadata.MD) (*grpc.ClientConn, error) {
	// if no destination override set then auto-detect from the metadata
	var destination = *destinationOverride
	if destination == "" {
		authority := md.Get(":authority")
		if len(authority) == 0 {
			return nil, fmt.Errorf("no destination override specified and could not auto-detect from dump")
		}
		destination = authority[0]
	}

	options := []grpc.DialOption{
		grpc.WithDefaultCallOptions(grpc.ForceCodec(grpc_proxy.NoopCodec{})),
		grpc.WithBlock(),
	}

	if tls.IsTLSRPC(md) {
		options = append(options, grpc.WithTransportCredentials(credentials.NewTLS(nil)))
	} else {
		options = append(options, grpc.WithInsecure())
	}

	dialCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return cachedConns.GetClientConn(dialCtx, destination, options...)
}