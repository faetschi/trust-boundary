//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"

	"tbound/supervisor/internal/piruntime"
)

func dialProductionPiService(ctx context.Context, socket piServiceSocketRegistration, serviceUID, serviceGID int,
	request piServiceRequest) (*net.UnixConn, error) {
	if ctx == nil || serviceUID <= 0 || serviceGID <= 0 ||
		validatePiServiceSocketRegistration(socket, serviceUID, serviceGID) != nil {
		return nil, ErrPiServiceUnavailable
	}
	allowed := piServicePeerCredentials{PID: os.Getpid(), UID: os.Geteuid(), GID: os.Getegid()}
	if !piServicePeerAllowed(socket, allowed) {
		return nil, ErrPiServiceRefused
	}
	socketIdentity, err := measurePiServiceSocketPath(socket, false)
	if err != nil {
		return nil, err
	}
	if err := validatePiServiceRequest(request); err != nil {
		return nil, err
	}
	dialer := net.Dialer{Timeout: piServiceReadTimeout}
	conn, err := dialer.DialContext(ctx, "unix", socket.SocketPath)
	if err != nil {
		return nil, errors.Join(ErrPiServiceUnavailable, err)
	}
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		_ = conn.Close()
		return nil, ErrPiServiceRefused
	}
	serverPeer, err := readPiServicePeer(unixConn)
	if err != nil || !piServiceServerAllowed(serverPeer, serviceUID, serviceGID) {
		_ = unixConn.Close()
		return nil, errors.Join(ErrPiServiceRefused, err)
	}
	if err := socketIdentity.revalidate(socket, false); err != nil {
		_ = unixConn.Close()
		return nil, errors.Join(ErrPiServiceRefused, err)
	}
	return unixConn, nil
}

func runProductionPiServiceVClient(ctx context.Context, input io.Reader, output io.Writer, request piServiceRequest) error {
	if ctx == nil || input == nil || output == nil || request.Role != corpusRoleV {
		return ErrPiServiceUnavailable
	}
	registration, err := piruntime.LoadVerifiedD09ManagedLiveRootPolicy(ctx)
	if err != nil {
		return errors.Join(ErrPiServiceUnavailable, err)
	}
	socket, contract, err := serviceSocketRegistrationFromSignedRegistration(registration)
	if err != nil {
		return err
	}
	serviceConfig, err := readProductionPiServiceConfig(contract.ServiceConfigPath, contract.ServiceConfigDigest)
	if err != nil {
		return err
	}
	if err := checkPiServiceClientRequest(serviceConfig, request); err != nil {
		return err
	}
	conn, err := dialProductionPiService(ctx, socket, registration.ServiceUID, registration.ServiceGID, request)
	if err != nil {
		return err
	}
	defer conn.Close()
	defer context.AfterFunc(ctx, func() { _ = conn.Close() })()
	encoded, err := json.Marshal(request)
	if err != nil {
		return err
	}
	if err := writePiServiceRequest(conn, encoded); err != nil {
		return err
	}
	for _, step := range []struct{ wait, want string }{{"ready", piServiceStatusReady}, {"final", "completed"}} {
		if step.wait == "final" {
			_, _ = io.Copy(io.Discard, input)
			if err := conn.CloseWrite(); err != nil {
				return err
			}
		}
		response, err := readPiServiceResponse(conn)
		if err != nil {
			return err
		}
		line, err := json.Marshal(response)
		if err != nil {
			return err
		}
		if _, err := output.Write(append(line, '\n')); err != nil {
			return err
		}
		if response.RequestID != request.RequestID || response.Status != step.want {
			return errors.New(piServiceRefusalMessage(response.Status, response.ErrorCode))
		}
	}
	return nil
}
