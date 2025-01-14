package noiseswarm

import (
	"context"
	mrand "math/rand"
	"sync"
	"time"

	"github.com/brendoncarroll/go-p2p"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

var _ p2p.SecureSwarm = &Swarm{}

const (
	// Overhead is the per message overhead.
	// MTU will be smaller than the underlying swarm's MTU by Overhead
	Overhead = 4 + 16
	// MaxDialAttempts is the maxmimum number of times to retry a handshake.
	MaxDialAttempts = 10
	// MaxDialBackoffDuration is the maximum time to wait between dial attempts
	MaxDialBackoffDuration = time.Second
)

type Swarm struct {
	swarm      p2p.Swarm
	privateKey p2p.PrivateKey
	localID    p2p.PeerID

	cf context.CancelFunc

	mu             sync.RWMutex
	lowerToSession map[sessionKey]*session
}

func New(x p2p.Swarm, privateKey p2p.PrivateKey) *Swarm {
	ctx, cf := context.WithCancel(context.Background())
	s := &Swarm{
		swarm:      x,
		privateKey: privateKey,
		localID:    p2p.NewPeerID(privateKey.Public()),

		cf: cf,

		lowerToSession: make(map[sessionKey]*session),
	}
	go s.cleanupLoop(ctx)
	return s
}

func (s *Swarm) Tell(ctx context.Context, addr p2p.Addr, data p2p.IOVec) error {
	dst := addr.(Addr)
	return s.withAnyReadySession(ctx, dst, func(sess *session) error {
		return sess.tell(ctx, p2p.VecBytes(data))
	})
}

func (s *Swarm) ServeTells(fn p2p.TellHandler) error {
	return s.swarm.ServeTells(func(msg *p2p.Message) {
		s.fromBelow(msg, fn)
	})
}

func (s *Swarm) Close() error {
	s.cf()
	return s.swarm.Close()
}

func (s *Swarm) LocalAddrs() (addrs []p2p.Addr) {
	for _, addr := range s.swarm.LocalAddrs() {
		addrs = append(addrs, Addr{
			ID:   p2p.NewPeerID(s.privateKey.Public()),
			Addr: addr,
		})
	}
	return addrs
}

func (s *Swarm) LookupPublicKey(ctx context.Context, addr p2p.Addr) (p2p.PublicKey, error) {
	target := addr.(Addr)
	sess := s.getAnyReadySession(target)
	if sess != nil {
		if sess.getRemotePeerID() == target.ID {
			return sess.getRemotePublicKey(), nil
		}
	}
	return nil, p2p.ErrPublicKeyNotFound
}

func (s *Swarm) PublicKey() p2p.PublicKey {
	return s.privateKey.Public()
}

func (s *Swarm) MTU(ctx context.Context, addr p2p.Addr) int {
	target := addr.(Addr)
	return s.swarm.MTU(ctx, target.Addr) - Overhead
}

func (s *Swarm) fromBelow(msg *p2p.Message, next p2p.TellHandler) {
	ctx := context.TODO()
	msg2, err := parseMessage(msg.Payload)
	if err != nil {
		logrus.Warn("noiseswarm got short message")
		return
	}
	initiator := msg2.getDirection() == directionRespToInit
	var up []byte
	for i := 0; i < 2; i++ {
		sess, _ := s.getOrCreateSession(msg.Src, initiator)
		up, err = sess.upward(ctx, msg2)
		if err != nil {
			if sess.isErrored() {
				s.deleteSession(msg.Src, sess)
				continue
			}
			return
		}
		if up != nil {
			next(&p2p.Message{
				Src: Addr{
					ID:   sess.getRemotePeerID(),
					Addr: msg.Src,
				},
				Dst: Addr{
					ID:   s.localID,
					Addr: msg.Dst,
				},
				Payload: up,
			})
		}
		break
	}
}

// withAnyReadySession calls fn with a non expired session, dialing a new one if necessary
// fn will only be called once, although dialSession may be called multiple times.
// fn will not be called until after the session is ready.
func (s *Swarm) withAnyReadySession(ctx context.Context, raddr Addr, fn func(s *session) error) error {
	// check the cache
	sess := s.getAnyReadySession(raddr)
	if sess != nil {
		actualPeerID := sess.getRemotePeerID()
		if actualPeerID != raddr.ID {
			s.deleteSession(raddr.Addr, sess)
			return errors.Errorf("wrong peer HAVE: %v WANT: %v", actualPeerID, raddr.ID)
		}
		return fn(sess)
	}
	// try dialing
	var err error
	for i := 0; i < MaxDialAttempts; i++ {
		sess, err := s.dialSession(ctx, raddr.Addr)
		if err == nil {
			actualPeerID := sess.getRemotePeerID()
			if actualPeerID != raddr.ID {
				s.deleteSession(raddr.Addr, sess)
				return errors.Errorf("wrong peer HAVE: %v WANT: %v", actualPeerID, raddr.ID)
			}
			return fn(sess)
		}
		time.Sleep(backoffTime(i, MaxDialBackoffDuration))
	}
	return err
}

// dialSession get's a session from the cache, or creates a new one.
// if a new session is created dialSession iniates a handshake and waits for it to complete or error.
func (s *Swarm) dialSession(ctx context.Context, lowerRaddr p2p.Addr) (*session, error) {
	sess, created := s.getOrCreateSession(lowerRaddr, true)
	if created {
		if err := sess.startHandshake(ctx); err != nil {
			s.deleteSession(lowerRaddr, sess)
			return nil, err
		}
	}
	if err := sess.waitReady(ctx); err != nil {
		return nil, err
	}
	return sess, nil
}

// getOrCreate session returns an existing session in the specified direction.
// if a new session is created it will return the session, and true otherwise false.
func (s *Swarm) getOrCreateSession(lowerRaddr p2p.Addr, initiator bool) (sess *session, created bool) {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	key := sessionKey{raddr: lowerRaddr.Key(), initiator: initiator}
	sess, exists := s.lowerToSession[key]
	if exists {
		if !sess.isExpired(now) && !sess.isErrored() {
			return sess, false
		}
	}
	sess = newSession(initiator, s.privateKey, func(ctx context.Context, data []byte) error {
		return s.swarm.Tell(ctx, lowerRaddr, p2p.IOVec{data})
	})
	s.lowerToSession[key] = sess
	return sess, true
}

// getAnyReadySession gets either an inbound or outbound session for an Addr
// it biases the outbound session if either handshake's handshake is not done.
func (s *Swarm) getAnyReadySession(raddr Addr) *session {
	outKey, inKey := makeSessionKeys(raddr.Addr)
	now := time.Now()
	s.mu.RLock()
	defer s.mu.RUnlock()
	outSess := s.lowerToSession[outKey]
	inSess := s.lowerToSession[inKey]
	sessions := []*session{outSess, inSess}
	for i, sess := range sessions {
		if sess == nil || sess.isExpired(now) || !sess.isReady() {
			sessions[i] = nil
		}
	}
	mrand.Shuffle(2, func(i, j int) {
		sessions[i], sessions[j] = sessions[j], sessions[i]
	})
	for _, sess := range sessions {
		if sess != nil {
			return sess
		}
	}
	return nil
}

// delete session deletes the session at lowerRaddr if it exists
// if a different session than x, or no session is found deleteSession is a noop
func (s *Swarm) deleteSession(lowerRaddr p2p.Addr, x *session) {
	key := sessionKey{
		raddr:     lowerRaddr.Key(),
		initiator: x.initiator,
	}
	s.mu.Lock()
	y := s.lowerToSession[key]
	if x == y {
		delete(s.lowerToSession, key)
	}
	s.mu.Unlock()
}

func (s *Swarm) cleanupLoop(ctx context.Context) {
	ticker := time.NewTicker(MaxSessionLife)
	defer ticker.Stop()
	for {
		now := time.Now()
		s.mu.Lock()
		for k, sess := range s.lowerToSession {
			if sess.isExpired(now) {
				delete(s.lowerToSession, k)
			}
		}
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func backoffTime(n int, max time.Duration) time.Duration {
	d := time.Millisecond * time.Duration(1<<n)
	if d > max {
		d = max
	}
	jitter := time.Duration(mrand.Intn(100))
	d = (d * jitter / 100) + d
	return d
}

type sessionKey struct {
	raddr     string
	initiator bool
}

// makeSessionKeys returns the 2 possible session keys for an address.
func makeSessionKeys(raddr p2p.Addr) (outbound, inbound sessionKey) {
	outbound = sessionKey{
		raddr:     raddr.Key(),
		initiator: true,
	}
	inbound = outbound
	inbound.initiator = false
	return outbound, inbound
}
