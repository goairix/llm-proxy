package main

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"go.uber.org/zap"
)

func TestShutdownApplicationOrderAndErrorIsolation(t *testing.T) {
	var order []string
	server := &recordingServer{order: &order, shutdownErr: errors.New("http failed")}
	controlPlane := &recordingControlPlane{order: &order, closeErr: errors.New("database failed")}
	telemetry := &recordingTelemetry{order: &order, err: errors.New("telemetry failed")}

	shutdownApplication(context.Background(), zap.NewNop(), server, controlPlane, telemetry)

	want := []string{"not_ready", "stop_reconnect", "http_shutdown", "database_close", "telemetry_shutdown"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("shutdown order = %v; want %v", order, want)
	}
}

type recordingServer struct {
	order       *[]string
	shutdownErr error
}

func (s *recordingServer) MarkNotReady() { *s.order = append(*s.order, "not_ready") }
func (s *recordingServer) Shutdown(context.Context) error {
	*s.order = append(*s.order, "http_shutdown")
	return s.shutdownErr
}

type recordingControlPlane struct {
	order    *[]string
	closeErr error
}

func (r *recordingControlPlane) Stop() { *r.order = append(*r.order, "stop_reconnect") }
func (r *recordingControlPlane) Close() error {
	*r.order = append(*r.order, "database_close")
	return r.closeErr
}

type recordingTelemetry struct {
	order *[]string
	err   error
}

func (r *recordingTelemetry) Shutdown(context.Context) error {
	*r.order = append(*r.order, "telemetry_shutdown")
	return r.err
}
