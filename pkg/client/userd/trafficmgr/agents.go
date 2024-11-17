package trafficmgr

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
)

func (s *session) watchAgentsLoop(ctx context.Context) error {
	stream, err := s.managerClient.WatchAgents(ctx, s.SessionInfo())
	if err != nil {
		return fmt.Errorf("manager.WatchAgents: %w", err)
	}
	for ctx.Err() == nil {
		snapshot, err := stream.Recv()
		if err != nil {
			// Handle as if we had an empty snapshot. This will ensure that port forwards and volume mounts are canceled correctly.
			s.setCurrentAgents(nil)
			if ctx.Err() != nil || errors.Is(err, io.EOF) {
				// Normal termination
				return nil
			}
			return fmt.Errorf("manager.WatchAgents recv: %w", err)
		}
		s.setCurrentAgents(snapshot.Agents)
	}
	return nil
}

func (s *session) getCurrentAgents() []*manager.AgentInfo {
	s.currentInterceptsLock.Lock()
	agents := s.currentAgents
	s.currentInterceptsLock.Unlock()
	return agents
}

func (s *session) setCurrentAgents(agents []*manager.AgentInfo) {
	s.currentInterceptsLock.Lock()
	s.currentAgents = agents
	s.currentInterceptsLock.Unlock()
}
