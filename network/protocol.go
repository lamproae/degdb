package network

import (
	"io"
	"time"

	"github.com/fatih/color"

	"github.com/degdb/degdb/protocol"
)

func (s *Server) handlePeerNotify(conn *Conn, msg *protocol.Message) {
	conn.peerRequest <- true
	peers := msg.GetPeerNotify().Peers
	for _, peer := range peers {
		s.peersLock.RLock()
		_, ok := s.Peers[peer.Id]
		s.peersLock.RUnlock()

		if ok {
			continue
		}

		s.peersLock.Lock()
		s.Peers[peer.Id] = nil
		s.peersLock.Unlock()

		if err := s.Connect(peer.Id); err != nil {
			s.Printf("ERR failed to connect to peer %s", err)
		}
	}
}

func (s *Server) handlePeerRequest(conn *Conn, msg *protocol.Message) {
	// TODO(d4l3k): Handle keyspace check.
	req := msg.GetPeerRequest()

	var peers []*protocol.Peer
	s.peersLock.RLock()
	for id, v := range s.Peers {
		if conn.Peer.Id == id || v == nil {
			continue
		}
		peers = append(peers, v.Peer)
		if req.Limit > 0 && int32(len(peers)) >= req.Limit {
			break
		}
	}
	s.peersLock.RUnlock()
	wrapper := &protocol.Message{Message: &protocol.Message_PeerNotify{
		PeerNotify: &protocol.PeerNotify{
			Peers: peers,
		}}}
	if err := conn.Send(wrapper); err != nil {
		s.Printf("ERR sending PeerNotify: %s", err)
	}
}

func (s *Server) handleHandshake(conn *Conn, msg *protocol.Message) {
	handshake := msg.GetHandshake()
	conn.Peer = handshake.GetSender()

	s.peersLock.RLock()
	peer := s.Peers[conn.Peer.Id]
	s.peersLock.RUnlock()

	if peer != nil {
		s.Printf("Ignoring duplicate peer %s.", conn.PrettyID())
		if err := conn.Close(); err != nil && err != io.EOF {
			s.Printf("ERR closing connection %s", err)
		}
		return
	}

	s.peersLock.Lock()
	s.Peers[conn.Peer.Id] = conn
	s.peersLock.Unlock()

	s.Print(color.GreenString("New peer %s", conn.PrettyID()))
	if handshake.Type == protocol.HANDSHAKE_INITIAL {
		if err := s.sendHandshake(conn, protocol.HANDSHAKE_RESPONSE); err != nil {
			s.Printf("ERR sendHandshake %s", err)
		}
	} else {
		if err := s.sendPeerRequest(conn); err != nil {
			s.Printf("ERR sendPeerRequest %s", err)
		}
	}
	go s.connHeartbeat(conn)
}

func (s *Server) connHeartbeat(conn *Conn) {
	ticker := time.NewTicker(time.Second * 60)
	for _ = range ticker.C {
		if conn.Closed {
			ticker.Stop()
			break
		}
		conn.peerRequestRetries = 0
		err := s.sendPeerRequest(conn)
		if err == io.EOF {
			ticker.Stop()
			break
		} else if err != nil {
			s.Printf("ERR sendPeerRequest %s", err)
		}
	}
}

func (s *Server) sendPeerRequest(conn *Conn) error {
	msg := &protocol.Message{Message: &protocol.Message_PeerRequest{
		PeerRequest: &protocol.PeerRequest{
		//Keyspace: s.LocalPeer().Keyspace,
		}}}
	conn.peerRequest = make(chan bool, 1)
	timeout := make(chan bool, 1)
	go func() {
		time.Sleep(10 * time.Second)
		timeout <- true
	}()
	go func() {
		select {
		case <-conn.peerRequest:
		case <-timeout:
			msg := color.RedString("Peer timed out! %s %+v", conn.PrettyID(), conn)
			conn.peerRequestRetries++
			if conn.peerRequestRetries >= 3 {
				s.peersLock.Lock()
				delete(s.Peers, conn.Peer.Id)
				s.peersLock.Unlock()

				conn.Close()
			} else {
				msg += "Retrying..."
				s.sendPeerRequest(conn)
			}
			s.Printf(msg)
		}
	}()
	if err := conn.Send(msg); err != nil {
		return err
	}
	return nil
}

func (s *Server) sendHandshake(conn *Conn, typ protocol.Handshake_Type) error {
	return conn.Send(&protocol.Message{
		Message: &protocol.Message_Handshake{
			Handshake: &protocol.Handshake{
				Type:   typ,
				Sender: s.LocalPeer(),
			},
		},
	})
}
