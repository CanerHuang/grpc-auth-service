package authServer

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strings"

	"authd/internal/config"
	"authd/internal/service"
	api "authd/pkg/grpc/auth"

	"github.com/rs/zerolog/log"
	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"
)

// Server 封裝 gRPC server 與其監聽器，負責啟動 serve 與停止時的資源釋放。
type Server struct {
	grpc           *grpc.Server
	listeners      []net.Listener
	unixSocketPath string // 非空表示有 UDS，Stop 時負責清除 socket 檔
	errCh          chan error
}

// NewServer 建立 gRPC server、依設定綁定監聽器（listenAddress 建 TCP、unixSocketPath
// 建 UDS，兩者可並存），並立即在各 listener 上開始 serve。回傳的 error 表示是否成功啟動；
// serve 期間的執行期錯誤改由 Err() 取得。
func NewServer(authService *service.Service, listenAddress, unixSocketPath string, ka config.KeepaliveConfig) (*Server, error) {
	listeners, err := buildListeners(listenAddress, unixSocketPath)
	if err != nil {
		return nil, err
	}

	g := grpc.NewServer(
		// 主動探活：閒置 ka.Time 後送 keepalive ping，ka.Timeout 內未收到回應即斷線，
		// 及早回收因網路中斷而殘留的半開連線。
		grpc.KeepaliveParams(keepalive.ServerParameters{
			Time:    ka.Time.Std(),
			Timeout: ka.Timeout.Std(),
		}),
		// 限制 client 端 keepalive ping 頻率：最短間隔 ka.MinTime，且允許在沒有
		// active stream 時送 ping；過於頻繁的 ping 會被視為違規而中斷連線。
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             ka.MinTime.Std(),
			PermitWithoutStream: true,
		}),
	)
	api.RegisterAuthAPIServer(g, NewHandler(authService))

	s := &Server{
		grpc:           g,
		listeners:      listeners,
		unixSocketPath: strings.TrimSpace(unixSocketPath),
		errCh:          make(chan error, len(listeners)),
	}

	for _, lis := range listeners {
		go func(lis net.Listener) {
			// Serve 在 GracefulStop 後會回傳 ErrServerStopped，視為正常結束。
			if err := s.grpc.Serve(lis); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
				s.errCh <- err
			}
		}(lis)
	}

	return s, nil
}

// Err 回傳 serve 期間發生的非預期錯誤 channel。
func (s *Server) Err() <-chan error {
	return s.errCh
}

// Stop 優雅停止 server 並釋放資源。GracefulStop 會關閉所有 listener，UDS listener
// 關閉時預設會自動 unlink socket 檔；此處再額外確保 socket 檔已被清除。
func (s *Server) Stop() {
	s.grpc.GracefulStop()
	if s.unixSocketPath != "" {
		if err := os.Remove(s.unixSocketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Warn().Err(err).Str("path", s.unixSocketPath).Msg("failed to remove unix socket file")
		}
	}
}

func buildListeners(listenAddress, unixSocketPath string) ([]net.Listener, error) {
	var listeners []net.Listener

	if addr := strings.TrimSpace(listenAddress); addr != "" {
		lis, err := net.Listen("tcp", addr)
		if err != nil {
			closeAll(listeners)
			return nil, fmt.Errorf("bind tcp %q failed: %w", addr, err)
		}
		listeners = append(listeners, lis)
		log.Info().Str("network", "tcp").Str("address", addr).Msg("gRPC listener bound")
	}

	if path := strings.TrimSpace(unixSocketPath); path != "" {
		if err := prepareSocketPath(path); err != nil {
			closeAll(listeners)
			return nil, err
		}
		lis, err := net.Listen("unix", path)
		if err != nil {
			closeAll(listeners)
			return nil, fmt.Errorf("bind unix %q failed: %w", path, err)
		}
		listeners = append(listeners, lis)
		log.Info().Str("network", "unix").Str("address", path).Msg("gRPC listener bound")
	}

	return listeners, nil
}

// prepareSocketPath 清除可重用的殘留 socket 檔；若該路徑已存在且「不是」socket，
// 則拒絕刪除並回傳錯誤，避免誤刪一般檔案或目錄。
func prepareSocketPath(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("stat unix socket %q failed: %w", path, err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("unix_socket_path %q already exists and is not a socket; refusing to remove", path)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove stale unix socket %q failed: %w", path, err)
	}
	return nil
}

func closeAll(listeners []net.Listener) {
	for _, lis := range listeners {
		lis.Close()
	}
}
