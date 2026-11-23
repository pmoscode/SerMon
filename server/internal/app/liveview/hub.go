// Package liveview relays the live view of processes ("top", KONZEPT §9, M3-14) from the agents
// to the browsers. Nothing of it is stored.
//
// The Hub of a server instance shares one agent stream per node among all its viewers: the
// first viewer starts it, every RenewEvery the lease of the agent (60 s) is renewed, StopAfter
// after the last viewer it is stopped. Only the instance holding the agent's session can start
// it (ADR 0015, Nachtrag M3-14); elsewhere the node counts as offline.
package liveview

import (
	"sync"
	"time"

	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// Timing of the agent streams.
const (
	// Interval is the time between two snapshots (KONZEPT §9).
	Interval = 2 * time.Second
	// RenewEvery renews the agent's lease (60 s) while someone watches.
	RenewEvery = 20 * time.Second
	// StopAfter stops the agent stream after the last viewer left.
	StopAfter = 30 * time.Second
)

// Summary is the header of a snapshot.
type Summary struct {
	CPUBusyRatio                      *float64
	CPUCount                          uint32
	MemoryTotalBytes, MemoryAvailable uint64
	SwapTotalBytes, SwapFreeBytes     uint64
	Load1, Load5, Load15              *float64
	UptimeSeconds                     *uint64
	Tasks                             Tasks
}

// Tasks are the processes per state.
type Tasks struct{ Total, Running, Sleeping, Stopped, Zombie uint32 }

// Process is one process; every text comes from the host, nil is "not readable".
type Process struct {
	PID, PPID        uint32
	UID              *uint32
	User, State      string
	CPURatio         *float64
	RSSBytes         *uint64
	MemoryRatio      *float64
	Threads          uint32
	RuntimeSeconds   *uint64
	Name             string
	Cmdline          *string
	CmdlineTruncated bool
	CmdlineHidden    bool
	KernelThread     bool
	Unit, Container  string
}

// Snapshot is the state of a host's processes at one moment.
type Snapshot struct {
	TakenAt   time.Time
	Summary   Summary
	Processes []Process
	Omitted   uint32
}

// Frame is what a viewer gets: a snapshot, the agent's refusal, or that the node is offline.
type Frame struct {
	Snapshot *Snapshot
	Refused  string
	Offline  bool
}

// Agents are the agent sessions of this instance.
type Agents interface {
	// StartLiveView starts or renews the node's live view; false if it is not connected here.
	StartLiveView(org tenant.OrgID, node identity.NodeID, interval time.Duration, hideCmdline bool) bool
	// StopLiveView ends it.
	StopLiveView(org tenant.OrgID, node identity.NodeID)
}

// Hub shares the agent streams of this instance.
type Hub struct {
	agents Agents
	mu     sync.Mutex
	feeds  map[identity.NodeID]*feed
}

type feed struct {
	org     tenant.OrgID
	viewers map[*Subscription]struct{}
	hide    bool
	running bool // the agent was started and has not refused or gone
	refused bool
	started time.Time // of the last start or renewal
	idle    time.Time // since when without viewers
}

// Subscription is one viewer.
type Subscription struct {
	hub    *Hub
	node   identity.NodeID
	frames chan Frame
	closed bool
}

// NewHub returns a hub sending its commands to agents.
func NewHub(agents Agents) *Hub {
	return &Hub{agents: agents, feeds: map[identity.NodeID]*feed{}}
}

// Frames delivers the frames; a slow viewer gets the newest.
func (s *Subscription) Frames() <-chan Frame { return s.frames }

// Close ends the viewing at now.
func (s *Subscription) Close(now time.Time) {
	h := s.hub
	h.mu.Lock()
	defer h.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	if f := h.feeds[s.node]; f != nil {
		delete(f.viewers, s)
		if len(f.viewers) == 0 {
			f.idle = now
		}
	}
}

// put hands a frame to the viewer, replacing one it hasn't taken yet.
func (s *Subscription) put(frame Frame) {
	for {
		select {
		case s.frames <- frame:
			return
		default:
		}
		select {
		case <-s.frames:
		default:
		}
	}
}

// Subscribe adds a viewer of the node; hide is the organization's switch for command lines.
func (h *Hub) Subscribe(org tenant.OrgID, node identity.NodeID, hide bool, now time.Time) *Subscription {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := &Subscription{hub: h, node: node, frames: make(chan Frame, 1)}
	f := h.feeds[node]
	if f == nil || f.org != org {
		f = &feed{org: org, viewers: map[*Subscription]struct{}{}}
		h.feeds[node] = f
	}
	f.viewers[s] = struct{}{}
	f.idle = time.Time{}
	if !f.running || f.hide != hide {
		f.hide, f.refused = hide, false
		h.start(node, f, now)
	}
	if !f.running {
		s.put(Frame{Offline: true})
	}
	return s
}

// start asks the agent (the caller holds the lock).
func (h *Hub) start(node identity.NodeID, f *feed, now time.Time) {
	f.running = h.agents.StartLiveView(f.org, node, Interval, f.hide)
	f.started = now
}

func (h *Hub) broadcast(f *feed, frame Frame) {
	for s := range f.viewers {
		s.put(frame)
	}
}

// Maintain renews the leases, retries offline nodes and stops streams without viewers.
func (h *Hub) Maintain(now time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for node, f := range h.feeds {
		switch {
		case len(f.viewers) == 0:
			if now.Sub(f.idle) < StopAfter {
				continue
			}
			if f.running {
				h.agents.StopLiveView(f.org, node)
			}
			delete(h.feeds, node)
		case f.refused:
		case now.Sub(f.started) >= RenewEvery:
			was := f.running
			h.start(node, f, now)
			if was && !f.running {
				h.broadcast(f, Frame{Offline: true})
			}
		}
	}
}

// Deliver passes a snapshot of the node's agent to its viewers; hidden command lines are removed
// here too.
func (h *Hub) Deliver(org tenant.OrgID, node identity.NodeID, snapshot Snapshot) {
	h.mu.Lock()
	defer h.mu.Unlock()
	f := h.feeds[node]
	if f == nil || f.org != org {
		return
	}
	if f.hide {
		processes := make([]Process, len(snapshot.Processes))
		for i, p := range snapshot.Processes {
			p.Cmdline, p.CmdlineTruncated, p.CmdlineHidden = nil, false, true
			processes[i] = p
		}
		snapshot.Processes = processes
	}
	h.broadcast(f, Frame{Snapshot: &snapshot})
}

// Refused passes the agent's refusal on; the stream is not tried again until a new viewer comes.
func (h *Hub) Refused(org tenant.OrgID, node identity.NodeID, reason string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	f := h.feeds[node]
	if f == nil || f.org != org {
		return
	}
	f.running, f.refused = false, true
	h.broadcast(f, Frame{Refused: reason})
}

// Connected restarts the stream of a node that connected while someone watches.
func (h *Hub) Connected(org tenant.OrgID, node identity.NodeID) {
	h.mu.Lock()
	defer h.mu.Unlock()
	f := h.feeds[node]
	if f == nil || f.org != org || len(f.viewers) == 0 {
		return
	}
	f.refused = false
	h.start(node, f, f.started)
}

// Disconnected tells the viewers that the node went offline.
func (h *Hub) Disconnected(org tenant.OrgID, node identity.NodeID) {
	h.mu.Lock()
	defer h.mu.Unlock()
	f := h.feeds[node]
	if f == nil || f.org != org {
		return
	}
	f.running = false
	h.broadcast(f, Frame{Offline: true})
}
