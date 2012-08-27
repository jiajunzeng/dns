package dns

import (
	"time"
)

// TODO: axfrreceive fixen, first can go
// a tsigTimersonly to responsewriter

// XfrMsg is used when doing [IA]xfr with a remote server.
type XfrMsg struct {
	RR    []RR          // the set of RRs in the answer section form the message of the server
	Rtt   time.Duration // round trip time  
	Error error         // if something went wrong, this contains the error  
}

// XfrReceive performs a [AI]xfr request (depends on the message's Qtype). It returns
// a channel of XfrMsg on which the replies from the server are sent. At the end of
// the transfer the channel is closed.
// It panics if the Qtype does not equal TypeAXFR or TypeIXFR. The messages are TSIG checked if
// needed, no other post-processing is performed. The caller must dissect the returned
// messages.
//
// Basic use pattern for receiving an AXFR:
//
//	// m contains the AXFR request
//	t, e := client.XfrReceive(m, "127.0.0.1:53")
//	for r := range t {
//		// ... deal with r.RR or r.Error
//	}
func (c *Client) XfrReceive(q *Msg, a string) (chan *XfrMsg, error) {
	w := new(reply)
	w.client = c
	w.addr = a
	w.req = q
	if err := w.dial(); err != nil {
		return nil, err
	}
	if err := w.send(q); err != nil {
		return nil, err
	}
	e := make(chan *XfrMsg)
	switch q.Question[0].Qtype {
	case TypeAXFR:
		go w.axfrReceive(e)
		return e, nil
	case TypeIXFR:
		go w.ixfrReceive(e)
		return e, nil
	default:
		return nil, ErrXfrType
	}
	panic("not reached")
}

func (w *reply) axfrReceive(c chan *XfrMsg) {
	first := true
	defer w.conn.Close()
	defer close(c)
	for {
		in, err := w.receive()
		if err != nil {
			c <- &XfrMsg{RR: in.Answer, Rtt: w.rtt, Error: err}
			return
		}
		if w.req.Id != in.Id {
			c <- &XfrMsg{RR: in.Answer, Rtt: w.rtt, Error: ErrId}
			return
		}
		if first {
			if !checkXfrSOA(in, true) {
				c <- &XfrMsg{RR: in.Answer, Rtt: w.rtt, Error: ErrXfrSoa}
				return
			}
			first = !first
		}

		if !first {
			w.tsigTimersOnly = true // Subsequent envelopes use this.
			if checkXfrSOA(in, false) {
				c <- &XfrMsg{RR: in.Answer, Rtt: w.rtt, Error: nil}
				return
			}
			c <- &XfrMsg{RR: in.Answer, Rtt: w.rtt, Error: nil}
		}
	}
	panic("not reached")
}

func (w *reply) ixfrReceive(c chan *XfrMsg) {
	var serial uint32 // The first serial seen is the current server serial
	first := true
	defer w.conn.Close()
	defer close(c)
	for {
		in, err := w.receive()
		if err != nil {
			c <- &XfrMsg{RR: in.Answer, Rtt: w.rtt, Error: err}
			return
		}
		if w.req.Id != in.Id {
			c <- &XfrMsg{RR: in.Answer, Rtt: w.rtt, Error: ErrId}
			return
		}
		if first {
			// A single SOA RR signals "no changes"
			if len(in.Answer) == 1 && checkXfrSOA(in, true) {
				c <- &XfrMsg{RR: in.Answer, Rtt: w.rtt, Error: nil}
				return
			}

			// Check if the returned answer is ok
			if !checkXfrSOA(in, true) {
				c <- &XfrMsg{RR: in.Answer, Rtt: w.rtt, Error: ErrXfrSoa}
				return
			}
			// This serial is important
			serial = in.Answer[0].(*RR_SOA).Serial
			first = !first
		}

		// Now we need to check each message for SOA records, to see what we need to do
		if !first {
			w.tsigTimersOnly = true
			// If the last record in the IXFR contains the servers' SOA,  we should quit
			if v, ok := in.Answer[len(in.Answer)-1].(*RR_SOA); ok {
				if v.Serial == serial {
					c <- &XfrMsg{RR: in.Answer, Rtt: w.rtt, Error: nil}
					return
				}
			}
			c <- &XfrMsg{RR: in.Answer, Rtt: w.rtt}
		}
	}
	panic("not reached")
}

// XfrSend performs an outgoing [IX]xfr depending on the request message. As
// long as the channel c is open ... TODO(mg): docs
// tsig is done, enveloping is done, voor de rest niks... TODO
func XfrSend(w ResponseWriter, req *Msg, c chan *XfrMsg) error {
	switch req.Question[0].Qtype {
	case TypeAXFR, TypeIXFR:
		go axfrSend(w, req, c)
	default:
		return ErrXfrType
	}
	return nil
}

// TODO(mg): count the RRs and the resulting size.
func axfrSend(w ResponseWriter, req *Msg, c chan *XfrMsg) {
	rep := new(Msg)
	rep.SetReply(req)
	rep.MsgHdr.Authoritative = true

	first := true
	w.TsigTimersOnly(false)
	for x := range c {
		// assume is fits
		rep.Answer = append(rep.Answer, x.RR...)
		w.Write(rep)
		if first {
			first = !first
			w.TsigTimersOnly(first)
		}
		rep.Answer = nil
	}
}

// Check if he SOA record exists in the Answer section of 
// the packet. If first is true the first RR must be a SOA
// if false, the last one should be a SOA.
func checkXfrSOA(in *Msg, first bool) bool {
	if len(in.Answer) > 0 {
		if first {
			return in.Answer[0].Header().Rrtype == TypeSOA
		} else {
			return in.Answer[len(in.Answer)-1].Header().Rrtype == TypeSOA
		}
	}
	return false
}
