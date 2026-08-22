package xray

import (
	"context"
	"net"
	"testing"

	fairShareService "github.com/xtls/xray-core/app/fairshare/command"
	"google.golang.org/grpc"
)

type fakeFairShareServer struct {
	fairShareService.UnimplementedFairShareServiceServer
	availBps uint64
}

func (f *fakeFairShareServer) SetNodeBandwidth(
	_ context.Context,
	request *fairShareService.SetNodeBandwidthRequest,
) (*fairShareService.SetNodeBandwidthResponse, error) {
	f.availBps = request.AvailBps
	return &fairShareService.SetNodeBandwidthResponse{}, nil
}

func TestSetNodeBandwidthConvertsBusinessBitsToRuntimeBytes(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	fake := &fakeFairShareServer{}
	fairShareService.RegisterFairShareServiceServer(server, fake)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)

	api := &XrayAPI{}
	if err := api.Init(listener.Addr().(*net.TCPAddr).Port); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(api.Close)
	if err := api.SetNodeBandwidth(800_000_000); err != nil {
		t.Fatal(err)
	}
	if fake.availBps != 100_000_000 {
		t.Fatalf("avail_bps = %d, want 100000000 bytes/s", fake.availBps)
	}
}
