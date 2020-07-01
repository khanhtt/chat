/******************************************************************************
 *
 *  Description :
 *    An isolated communication channel (chat room, 1:1 conversation) for
 *    usualy multiple users. There is no communication across topics.
 *
 *****************************************************************************/

package main

import (
	"encoding/json"
	"errors"
	"log"
	"strings"
	"sync/atomic"
	"time"

	"github.com/khanhtt/chat/server/auth"
	"github.com/khanhtt/chat/server/push"
	"github.com/khanhtt/chat/server/store"
	"github.com/khanhtt/chat/server/store/types"
)

const UA_TIMER_DELAY = time.Second * 5

// Maximum number of SeqIds to pass in a list
const MAX_SEQ_COUNT = 128

// Topic: an isolated communication channel
type Topic struct {
	// Еxpanded/unique name of the topic.
	name string
	// For single-user topics session-specific topic name, such as 'me',
	// otherwise the same as 'name'.
	x_original string

	// Topic category
	cat types.TopicCat

	// TODO(gene): currently unused
	// If isProxy == true, the actual topic is hosted by another cluster member.
	// The topic should:
	// 1. forward all messages to master
	// 2. route replies from the master to sessions.
	// 3. disconnect sessions at master's request.
	// 4. shut down the topic at master's request.
	isProxy bool

	// Time when the topic was first created
	created time.Time
	// Time when the topic was last updated
	updated time.Time

	// Server-side ID of the last data message
	lastId int
	// If messages were hard-deleted, the ID of the last deleted meassage
	clearId int

	// Last published userAgent ('me' topic only)
	userAgent string

	// User ID of the topic owner/creator. Could be zero.
	owner types.Uid

	// Default access mode
	accessAuth types.AccessMode
	accessAnon types.AccessMode

	// Topic's public data
	public interface{}

	// Topic's per-subscriber data
	perUser map[types.Uid]perUserData
	// User's contact list (not nil for 'me' topic only).
	// The map keys are UserIds for P2P topics and grpXXX for group topics.
	perSubs map[string]perSubsData

	// Sessions attached to this topic
	sessions map[*Session]bool

	// Inbound {data} and {pres} messages from sessions or other topics, already converted to SCM. Buffered = 256
	broadcast chan *ServerComMessage

	// Channel for receiving {get}/{set} requests, buffered = 32
	meta chan *metaReq

	// Subscribe requests from sessions, buffered = 32
	reg chan *sessionJoin

	// Unsubscribe requests from sessions, buffered = 32
	unreg chan *sessionLeave

	// Track the most active sessions to report User Agent changes. Buffered = 32
	uaChange chan string

	// Channel to terminate topic  -- either the topic is deleted or system is being shut down. Buffered = 1.
	exit chan *shutDown
	// Flag which tells topic to stop acception requests: hub is in the process of shutting it down
	suspended atomicBool
}

type atomicBool int32

// perUserData holds topic's cache of per-subscriber data
type perUserData struct {
	// Timestamps when the subscription was created and updated
	created time.Time
	updated time.Time

	online int

	// Last t.lastId reported by user through {pres} as received or read
	recvId int
	readId int
	// Greatest ID of a soft-deleted message
	clearId int

	private interface{}

	modeWant  types.AccessMode
	modeGiven types.AccessMode

	// P2P only:
	public    interface{}
	topicName string
}

// perSubsData holds user's (on 'me' topic) cache of subscription data
type perSubsData struct {
	online bool
}

// Session wants to leave the topic
type sessionLeave struct {
	// Session which initiated the request
	sess *Session
	// Leave and unsubscribe
	unsub bool
	// Topic to report success of failure on
	topic string
	// ID of originating request, if any
	reqId string
}

const (
	StopNone = iota
	StopShutdown
	StopDeleted
	StopRehashing
)

// Topic shutdown
type shutDown struct {
	// Channel to report back completion of topic shutdown. Could be nil
	done chan<- bool
	// Topic is being deleted as opposite to total system shutdown
	reason int
}

type pushReceipt struct {
	rcpt   *push.Receipt
	uidMap map[types.Uid]int
}

var nilPresParams = &PresParams{}

func (t *Topic) run(hub *Hub) {

	log.Printf("Topic started: '%s'", t.name)

	keepAlive := TOPICTIMEOUT // TODO(gene): read keepalive value from the command line
	killTimer := time.NewTimer(time.Hour)
	killTimer.Stop()

	// 'me' only
	var uaTimer *time.Timer
	var currentUA string
	uaTimer = time.NewTimer(time.Minute)
	uaTimer.Stop()

	for {
		select {
		case sreg := <-t.reg:
			// Request to add a conection to this topic

			if t.isSuspended() {
				sreg.sess.queueOut(ErrLocked(sreg.pkt.Id, t.original(sreg.sess.uid), types.TimeNow()))
			} else {
				// The topic is alive, so stop the kill timer, if it's ticking. We don't want the topic to die
				// while processing the call
				killTimer.Stop()
				if err := t.handleSubscription(hub, sreg); err == nil {
					// give a broadcast channel to the connection (.read)
					// give channel to use when shutting down (.done)
					sreg.sess.subs[t.name] = &Subscription{
						broadcast: t.broadcast,
						done:      t.unreg,
						meta:      t.meta,
						uaChange:  t.uaChange}

					t.sessions[sreg.sess] = true

				} else if len(t.sessions) == 0 {
					// Failed to subscribe, the topic is still inactive
					killTimer.Reset(keepAlive)
				}
			}

		case leave := <-t.unreg:
			// Remove connection from topic; session may continue to function
			now := types.TimeNow()

			if t.isSuspended() {
				leave.sess.queueOut(ErrLocked(leave.reqId, t.original(leave.sess.uid), now))
				continue

			} else if leave.unsub {
				// User wants to leave and unsubscribe.
				if err := t.replyLeaveUnsub(hub, leave.sess, leave.reqId); err != nil {
					log.Println("failed to unsub", err)
					continue
				}

			} else {
				// Just leaving the topic without unsubscribing
				delete(t.sessions, leave.sess)

				pud := t.perUser[leave.sess.uid]
				pud.online--
				if t.cat == types.TopicCat_Me {
					mrs := t.mostRecentSession()
					if mrs == nil {
						// Last session
						mrs = leave.sess
					} else {
						// Change UA to the most recent live session and announce it. Don't block.
						select {
						case t.uaChange <- mrs.userAgent:
						default:
						}
					}
					// Update user's last online timestamp & user agent
					if err := store.Users.UpdateLastSeen(mrs.uid, mrs.userAgent, now); err != nil {
						log.Println(err)
					}
				} else if t.cat == types.TopicCat_Grp && pud.online == 0 {
					// User is going offline: notify online subscribers on 'me'
					t.presSubsOnline("off", leave.sess.uid.UserId(), nilPresParams,
						types.ModeRead, "")
				}

				t.perUser[leave.sess.uid] = pud

				if leave.reqId != "" {
					leave.sess.queueOut(NoErr(leave.reqId, t.original(leave.sess.uid), now))
				}
			}

			// If there are no more subscriptions to this topic, start a kill timer
			if len(t.sessions) == 0 {
				killTimer.Reset(keepAlive)
			}

		case msg := <-t.broadcast:
			// Content message intended for broadcasting to recepients

			var pushRcpt *pushReceipt

			if msg.Data != nil {
				if t.isSuspended() {
					if msg.sessFrom != nil {
						msg.sessFrom.queueOut(ErrLocked(msg.id, t.original(msg.sessFrom.uid), msg.timestamp))
					}
					continue
				}

				from := types.ParseUserId(msg.Data.From)
				userData := t.perUser[from]

				// msg.sessFrom is not nil when the message originated at the client.
				// for internally generated messages the akn is nil
				if msg.sessFrom != nil {
					if !(userData.modeWant & userData.modeGiven).IsWriter() {
						msg.sessFrom.queueOut(ErrPermissionDenied(msg.id, t.original(msg.sessFrom.uid),
							msg.timestamp))
						continue
					}
				}

				if err := store.Messages.Save(&types.Message{
					ObjHeader: types.ObjHeader{CreatedAt: msg.Data.Timestamp},
					SeqId:     t.lastId + 1,
					Topic:     t.name,
					From:      from.String(),
					Head:      msg.Data.Head,
					Content:   msg.Data.Content}); err != nil {

					log.Printf("topic[%s]: failed to save message: %v", t.name, err)
					msg.sessFrom.queueOut(ErrUnknown(msg.id, t.original(msg.sessFrom.uid), msg.timestamp))

					continue
				}

				t.lastId++
				msg.Data.SeqId = t.lastId

				if msg.id != "" {
					reply := NoErrAccepted(msg.id, t.original(msg.sessFrom.uid), msg.timestamp)
					reply.Ctrl.Params = map[string]int{"seq": t.lastId}
					msg.sessFrom.queueOut(reply)
				}

				pushRcpt = t.makePushReceipt(msg.Data)

				// Message sent: notify offline 'R' subscrbers on 'me'
				t.presSubsOffline("msg", &PresParams{seqId: t.lastId}, types.ModeRead, "", true)

			} else if msg.Pres != nil {

				t.presProcReq(msg.Pres.Src, msg.Pres.What, msg.Pres.wantReply)
				if t.x_original != msg.Pres.Topic || strings.HasPrefix(msg.Pres.What, "?") {
					// This is just a request for status, don't forward it to sessions
					continue
				}
			} else if msg.Info != nil {
				if t.isSuspended() {
					// Ignore info messages - topic is being deleted
					continue
				}

				if msg.Info.SeqId > t.lastId {
					// Drop bogus read notification
					continue
				}

				uid := types.ParseUserId(msg.Info.From)
				pud := t.perUser[uid]

				// Filter out "kp" from users with no 'W' permission
				if msg.Info.What == "kp" && !(pud.modeGiven & pud.modeWant).IsWriter() {
					continue
				}

				if msg.Info.What == "read" || msg.Info.What == "recv" {
					// Filter out "read/recv" from users with no 'R' permission
					if !(pud.modeGiven & pud.modeWant).IsReader() {
						continue
					}

					var read, recv int
					if msg.Info.What == "read" {
						if msg.Info.SeqId > pud.readId {
							pud.readId = msg.Info.SeqId
							read = pud.readId
						} else {
							// No need to report stale or bogus read status
							continue
						}
					} else if msg.Info.What == "recv" {
						if msg.Info.SeqId > pud.recvId {
							pud.recvId = msg.Info.SeqId
							recv = pud.recvId
						} else {
							continue
						}
					}

					if pud.readId > pud.recvId {
						pud.recvId = pud.readId
						recv = pud.recvId
					}

					if err := store.Subs.Update(t.name, uid,
						map[string]interface{}{
							"RecvSeqId": pud.recvId,
							"ReadSeqId": pud.readId}); err != nil {

						log.Printf("topic[%s]: failed to update SeqRead/Recv counter: %v", t.name, err)
						continue
					}

					// Read/recv updated: notify user's other sessions of the change
					t.presPubMessageCount(uid, nil, 0, recv, read, msg.skipSid)

					t.perUser[uid] = pud
				}
			}

			// Broadcast the message. Only {data}, {pres}, {info} are broadcastable.
			// {meta} and {ctrl} are sent to the session only
			if msg.Data != nil || msg.Pres != nil || msg.Info != nil {

				var packet []byte
				if t.cat != types.TopicCat_P2P {
					packet, _ = json.Marshal(msg)
				}

				for sess := range t.sessions {
					if sess.sid == msg.skipSid {
						continue
					}

					if msg.Pres != nil {
						// Skip notifying - already notified on topic.
						if msg.Pres.skipTopic != "" && sess.subs[msg.Pres.skipTopic] != nil {
							continue
						}

						// Check presence filters
						pud, _ := t.perUser[sess.uid]
						if !(pud.modeGiven & pud.modeWant).IsPresencer() ||
							(msg.Pres.filter != 0 && int(pud.modeGiven&pud.modeWant)&msg.Pres.filter == 0) {
							continue
						}
					} else {
						// Check if the user has Read permission
						pud, _ := t.perUser[sess.uid]
						if !(pud.modeGiven & pud.modeWant).IsReader() {
							continue
						}
					}

					if t.cat == types.TopicCat_P2P {
						// For p2p topics topic name is dependent on receiver
						if msg.Data != nil {
							msg.Data.Topic = t.original(sess.uid)
						} else if msg.Pres != nil {
							msg.Pres.Topic = t.original(sess.uid)
						} else if msg.Info != nil {
							msg.Info.Topic = t.original(sess.uid)
						}
						packet, _ = json.Marshal(msg)
					}

					select {
					case sess.send <- packet:
						// Update device map with the device ID which should recive the notification
						if pushRcpt != nil {
							if i, ok := pushRcpt.uidMap[sess.uid]; ok {
								pushRcpt.rcpt.To[i].Delieved++
								if sess.deviceId != "" {
									pushRcpt.rcpt.To[i].Devices = append(pushRcpt.rcpt.To[i].Devices, sess.deviceId)
								}
							}
						}
					default:
						log.Printf("topic[%s]: connection stuck, detaching", t.name)
						t.unreg <- &sessionLeave{sess: sess, unsub: false}
					}
				}

				if pushRcpt != nil {
					push.Push(pushRcpt.rcpt)
				}

			} else {
				// TODO(gene): remove this
				log.Panic("topic[%s]: wrong message type for broadcasting", t.name)
			}

		case meta := <-t.meta:
			// log.Printf("topic[%s]: got meta message '%#+v' %x", t.name, meta, meta.what)

			// Request to get/set topic metadata
			if meta.pkt.Get != nil {
				// Get request
				if meta.what&constMsgMetaDesc != 0 {
					t.replyGetDesc(meta.sess, meta.pkt.Get.Id, "", meta.pkt.Get.Desc)
				}
				if meta.what&constMsgMetaSub != 0 {
					t.replyGetSub(meta.sess, meta.pkt.Get.Id, meta.pkt.Get.Sub)
				}
				if meta.what&constMsgMetaData != 0 {
					t.replyGetData(meta.sess, meta.pkt.Get.Id, meta.pkt.Get.Data)
				}
			} else if meta.pkt.Set != nil {
				// Set request
				if meta.what&constMsgMetaDesc != 0 {
					t.replySetDesc(meta.sess, meta.pkt.Set)
				}
				if meta.what&constMsgMetaSub != 0 {
					t.replySetSub(hub, meta.sess, meta.pkt.Set)
				}

			} else if meta.pkt.Del != nil {
				// Del request
				switch meta.what {
				case constMsgDelMsg:
					t.replyDelMsg(meta.sess, meta.pkt.Del)
				case constMsgDelSub:
					t.replyDelSub(hub, meta.sess, meta.pkt.Del)
				case constMsgDelTopic:
					t.replyDelTopic(hub, meta.sess, meta.pkt.Del)
				}
			}
		case ua := <-t.uaChange:
			// process an update to user agent from one of the sessions
			currentUA = ua
			uaTimer.Reset(UA_TIMER_DELAY)

		case <-uaTimer.C:
			// Publish user agent changes after a delay
			if currentUA == "" || currentUA == t.userAgent {
				continue
			}
			t.userAgent = currentUA
			t.presUsersOfInterest("ua", t.userAgent)

		case <-killTimer.C:
			// Topic timeout
			hub.unreg <- &topicUnreg{topic: t.name}
			if t.cat == types.TopicCat_Me {
				uaTimer.Stop()
				t.presUsersOfInterest("off", currentUA)
			} else if t.cat == types.TopicCat_Grp {
				t.presSubsOffline("off", nilPresParams, 0, "", false)
			}
			return

		case sd := <-t.exit:
			// Handle four cases:
			// 1. Topic is shutting down by timer due to inactivity (reason == StopNone)
			// 2. Topic is being deleted (reason == StopDeleted)
			// 3. System shutdown (reason == StopShutdown, done != nil).
			// 4. Cluster rehashing (reason == StopRehashing)
			// FIXME(gene): save lastMessage value;

			if t.cat == types.TopicCat_Grp && sd.reason == StopDeleted {
				t.presSubsOffline("gone", nilPresParams, 0, "", false)
				// Not publishing online/offline to deleted P2P topics
			} else if sd.reason == StopRehashing {
				// Must send individual messages to sessions because normal sending through the topic's
				// broadcast channel won't work - it will be shut down too soon.
				t.presSubsOnlineDirect("term")
			}

			// In case of a system shutdown don't bother with notifications. They won't be delivered anyway.

			// Report completion back to sender, if 'done' is not nil.
			if sd.done != nil {
				sd.done <- true
			}
			return
		}
	}
}

// Session subscribed to a topic, created == true if topic was just created and {pres} needs to be announced
func (t *Topic) handleSubscription(h *Hub, sreg *sessionJoin) error {
	var getWhat = 0
	if sreg.pkt.Get != nil {
		getWhat = parseMsgClientMeta(sreg.pkt.Get.What)
	}

	if err := t.subCommonReply(h, sreg, (getWhat&constMsgMetaDesc != 0)); err != nil {
		return err
	}

	pud := t.perUser[sreg.sess.uid]
	if sreg.loaded {
		// Notify user's contact that the given user is online now.
		if t.cat == types.TopicCat_Me {
			if err := t.loadContacts(sreg.sess.uid); err != nil {
				log.Println("topic: failed to load contacts", t.name, err.Error())
			}
			// User online: notify users of interest
			t.presUsersOfInterest("on", sreg.sess.userAgent)
		} else if t.cat == types.TopicCat_Grp || t.cat == types.TopicCat_P2P {
			if sreg.created {
				// Notify creator's other sessions that the topic was created.
				t.presSingleUserOffline(sreg.sess.uid, "acs",
					&PresParams{
						dWant:  types.ModeNone.Delta(pud.modeWant),
						dGiven: types.ModeNone.Delta(pud.modeGiven),
						actor:  "me"},
					sreg.sess.sid, false)

				// Special handling of a P2P topic - notifying the other
				// participant.
				if t.cat == types.TopicCat_P2P {
					user2 := t.p2pOtherUser(sreg.sess.uid)
					pud2 := t.perUser[user2]

					// Inform the other user that the topic was just created
					t.presSingleUserOffline(user2, "acs", &PresParams{
						dWant:  types.ModeNone.Delta(pud2.modeWant),
						dGiven: types.ModeNone.Delta(pud2.modeGiven),
						actor:  sreg.sess.uid.UserId()}, "", false)

					// Initiate exchange of 'online' status with the other user.
					// We don't know if the current user is online in the 'me' topic,
					// so sending an '?unkn' status to user2. His 'me' topic
					// will report user2's status and request an actual status from user1.
					t.presSingleUserOffline(user2, "?unkn", nilPresParams, "", false)
				}
			}

			if t.cat == types.TopicCat_Grp {
				// Notify topic subscribers that the topic is online now.
				t.presSubsOffline("on", nilPresParams, 0, "", false)
			}
		}
	} else if t.cat == types.TopicCat_Grp && pud.online == 1 {
		// User just joined. Notify other group members
		t.presSubsOnline("on", sreg.sess.uid.UserId(), nilPresParams, types.ModeRead, sreg.sess.sid)
	}

	if getWhat&constMsgMetaSub != 0 {
		// Send get.sub response as a separate {meta} packet
		t.replyGetSub(sreg.sess, sreg.pkt.Id, sreg.pkt.Get.Sub)
	}

	if getWhat&constMsgMetaData != 0 {
		// Send get.data response as {data} packets
		t.replyGetData(sreg.sess, sreg.pkt.Id, sreg.pkt.Get.Data)
	}
	return nil
}

// subCommonReply generates a response to a subscription request
func (t *Topic) subCommonReply(h *Hub, sreg *sessionJoin, sendDesc bool) error {
	var now time.Time
	// For newly created topics report topic creation time.
	if sreg.created {
		now = t.updated
	} else {
		now = types.TimeNow()
	}

	// The topic is already initialized by the Hub

	var private interface{}
	var mode string

	if sreg.pkt.Set != nil {
		if sreg.pkt.Set.Sub != nil {
			if sreg.pkt.Set.Sub.User != "" {
				log.Println("subCommonReply: UID in request, msg.Sub.Sub.User=", sreg.pkt.Set.Sub.User)
				sreg.sess.queueOut(ErrMalformed(sreg.pkt.Id, t.original(sreg.sess.uid), now))
				return errors.New("user id must not be specified")
			}

			mode = sreg.pkt.Set.Sub.Mode
		}

		if sreg.pkt.Set.Desc != nil && !isNullValue(sreg.pkt.Set.Desc.Private) {
			private = sreg.pkt.Set.Desc.Private
		}
	}

	// Create new subscription or modify an existing one.
	if err := t.requestSub(h, sreg.sess, sreg.pkt.Id, mode, private); err != nil {
		log.Println("requestSub failed: ", err.Error())
		return err
	}

	pud := t.perUser[sreg.sess.uid]
	pud.online++
	t.perUser[sreg.sess.uid] = pud

	resp := NoErr(sreg.pkt.Id, t.original(sreg.sess.uid), now)
	// Report access mode.
	resp.Ctrl.Params = map[string]MsgAccessMode{"acs": {
		Given: pud.modeGiven.String(),
		Want:  pud.modeWant.String(),
		Mode:  (pud.modeGiven & pud.modeWant).String()}}
	sreg.sess.queueOut(resp)

	if sendDesc {
		var tmpName string
		if sreg.created {
			tmpName = sreg.pkt.Topic
		}
		t.replyGetDesc(sreg.sess, sreg.pkt.Id, tmpName, sreg.pkt.Get.Desc)
	}

	return nil
}

// User requests or updates a self-subscription to a topic. Called as a
// result of {sub} or {meta set=sub}.
//
//	h 		- hub
//	sess 	- originating session
//  pktId 	- originating packet Id
//	want	- requested access mode
//	info 	- explanation info given by the requester
//	private	- private value to assign to the subscription
// Handle these cases:
// A. User is trying to subscribe for the first time (no subscription)
// B. User is already subscribed, just joining without changing anything
// C. User is responsing to an earlier invite (modeWant was "N" in subscription)
// D. User is already subscribed, changing modeWant
// E. User is accepting ownership transfer (requesting ownership transfer is not permitted)
func (t *Topic) requestSub(h *Hub, sess *Session, pktId string, want string,
	private interface{}) error {

	now := types.TimeNow()

	// Access mode values as they were before this request was processed.
	oldWant := types.ModeNone
	oldGiven := types.ModeNone

	log.Println("requestSub", t.name, "'", want, "'")

	// Parse access mode requested by the user
	modeWant := types.ModeUnset
	if want != "" {
		if err := modeWant.UnmarshalText([]byte(want)); err != nil {
			log.Println(err.Error())
			sess.queueOut(ErrMalformed(pktId, t.original(sess.uid), now))
			return err
		}
	}

	// Check if it's an attempt at a new subscription to the topic.
	// It could be an actual subscription (IsJoiner() == true) or a ban (IsJoiner() == false)
	userData, existingSub := t.perUser[sess.uid]
	if !existingSub {

		userData.private = private

		if t.cat == types.TopicCat_P2P {
			// If it's a re-subscription to a p2p topic, set public and permissions

			// t.perUser contains just one element - the other user
			for uid2, user2Data := range t.perUser {
				if user2, err := store.Users.Get(uid2); err != nil {
					log.Println(err.Error())
					sess.queueOut(ErrUnknown(pktId, t.original(sess.uid), now))
					return err
				} else if user2 == nil {
					sess.queueOut(ErrUserNotFound(pktId, t.original(sess.uid), now))
					return errors.New("user not found")
				} else {
					userData.public = user2.Public
					userData.topicName = uid2.UserId()
					userData.modeGiven = selectAccessMode(sess.authLvl,
						user2.Access.Anon, user2.Access.Auth, types.ModeCP2P)
					if modeWant == types.ModeUnset {
						// By default give user1 the same thing user1 gave to user2.
						userData.modeWant = user2Data.modeGiven
					} else {
						userData.modeWant = modeWant
					}
				}
				break
			}

			// Make sure the user is not asking for unreasonable permissions
			userData.modeWant = (userData.modeWant & types.ModeCP2P) | types.ModeApprove
		} else {
			// For non-p2p2 topics access is given as default access
			userData.modeGiven = t.accessFor(sess.authLvl)

			if modeWant == types.ModeUnset {
				// User wants default access mode.
				userData.modeWant = t.accessFor(sess.authLvl)
			} else {
				userData.modeWant = modeWant
			}
		}

		// Add subscription to database
		sub := &types.Subscription{
			User:      sess.uid.String(),
			Topic:     t.name,
			ModeWant:  userData.modeWant,
			ModeGiven: userData.modeGiven,
			Private:   userData.private,
		}

		if err := store.Subs.Create(sub); err != nil {
			log.Println(err.Error())
			sess.queueOut(ErrUnknown(pktId, t.original(sess.uid), now))
			return err
		}
	} else {
		// Process update to existing subscription. It could be an incomplete subscription for a new topic.

		var ownerChange bool

		// Save old access values

		oldWant = userData.modeWant
		oldGiven = userData.modeGiven

		if modeWant != types.ModeUnset {
			// Explicit modeWant is provided

			// Perform sanity checks
			if userData.modeGiven.IsOwner() {
				// Check for possible ownership transfer. Handle the following cases:
				// 1. Owner joining the topic without any changes
				// 2. Owner changing own settings
				// 3. Acceptance or rejection of the ownership transfer

				// Make sure the current owner cannot unset the owner flag or ban himself
				if t.owner == sess.uid && !modeWant.IsOwner() {
					log.Println("requestSub: owner attempts to unset the owner flag")
					sess.queueOut(ErrPermissionDenied(pktId, t.original(sess.uid), now))
					return errors.New("cannot unset ownership")
				}

				// Ownership transfer
				ownerChange = modeWant.IsOwner() && !userData.modeWant.IsOwner()

				// The owner should be able to grant himself any access permissions
				// If ownership transfer is rejected don't upgrade
				if modeWant.IsOwner() && !userData.modeGiven.BetterEqual(modeWant) {
					userData.modeGiven |= modeWant
				}
			} else if modeWant.IsOwner() {
				// Ownership transfer can only be initiated by the owner
				sess.queueOut(ErrPermissionDenied(pktId, t.original(sess.uid), now))
				return errors.New("non-owner cannot request ownership transfer")
			} else if t.cat == types.TopicCat_P2P {
				// For P2P topics ignore requests for 'D'. Otherwise it will generate a useless announcement
				modeWant = (modeWant & types.ModeCP2P) | types.ModeApprove
			} else if userData.modeGiven.IsAdmin() && modeWant.IsAdmin() {
				// The Admin should be able to grant any permissions except ownership (checked previously) &
				// hard-deleting messages.
				if !userData.modeGiven.BetterEqual(modeWant & ^types.ModeDelete) {
					userData.modeGiven |= (modeWant & ^types.ModeDelete)
				}
			}
		}

		// If user has not requested a new access mode, provide one by default.
		if modeWant == types.ModeUnset {
			// If the user has self-banned before, un-self-ban. Otherwise do not make a change.
			if !userData.modeWant.IsJoiner() {
				userData.modeWant = t.accessFor(sess.authLvl)
			}
		} else if userData.modeWant != modeWant {
			// The user has provided a new modeWant and it' different from the one before
			userData.modeWant = modeWant
		}

		// Save changes to DB
		if userData.modeWant != oldWant || userData.modeGiven != oldGiven {
			update := map[string]interface{}{}
			// FIXME(gene): gorethink has a bug which causes ModeXYZ to be saved as a string, converting to int
			if userData.modeWant != oldWant {
				update["ModeWant"] = int(userData.modeWant)
			}
			if userData.modeGiven != oldGiven {
				update["ModeGiven"] = int(userData.modeGiven)
			}
			if err := store.Subs.Update(t.name, sess.uid, update); err != nil {
				sess.queueOut(ErrUnknown(pktId, t.original(sess.uid), now))
				return err
			}
		}

		// No transactions in RethinkDB, but two owners are better than none
		if ownerChange {

			oldOwnerData := t.perUser[t.owner]
			oldOwnerData.modeGiven = (oldOwnerData.modeGiven & ^types.ModeOwner)
			oldOwnerData.modeWant = (oldOwnerData.modeWant & ^types.ModeOwner)
			if err := store.Subs.Update(t.name, t.owner,
				// FIXME(gene): gorethink has a bug which causes ModeXYZ to be saved as a string, converting to int
				map[string]interface{}{
					"ModeWant":  int(oldOwnerData.modeWant),
					"ModeGiven": int(oldOwnerData.modeGiven)}); err != nil {
				return err
			}
			t.perUser[t.owner] = oldOwnerData
			t.owner = sess.uid
		}
	}

	t.perUser[sess.uid] = userData

	// If the user is self-banning himself from the topic, no action is needed.
	// Re-subscription will unban.
	if !userData.modeWant.IsJoiner() {
		t.evictUser(sess.uid, false, "")
		// The callee will send NoErrOK
		return nil
	} else if !userData.modeGiven.IsJoiner() {
		// User was banned
		log.Println("User is banned", t.name, sess.uid.UserId(), userData.modeGiven.String(), oldGiven.String())

		sess.queueOut(ErrPermissionDenied(pktId, t.original(sess.uid), now))
		return errors.New("topic access denied")
	}

	// If something has changed and the requested access mode is different from the given.
	// This will not send a notification for a newly created topic because Hub sets the same values for
	// the old want/given.
	if userData.modeWant != oldWant || userData.modeGiven != oldGiven {
		params := &PresParams{
			actor:  sess.uid.UserId(),
			dWant:  oldWant.Delta(userData.modeWant),
			dGiven: oldGiven.Delta(userData.modeGiven)}

		// Announce to the admins who are online in the topic.
		t.presSubsOnline("acs", sess.uid.UserId(), params, types.ModeCSharer, sess.sid)

		// If it's a new subscription or if the user asked for permissions in excess of what was granted,
		// announce the request to topic admins on 'me' as well.
		var adminsNotified bool
		if !userData.modeGiven.BetterEqual(userData.modeWant) || !existingSub {
			t.presSubsOffline("acs", params, types.ModeCSharer, sess.sid, true)
			adminsNotified = true
		}

		if !adminsNotified || !(userData.modeWant & userData.modeGiven).IsSharer() {
			// Notify requester's other sessions.
			// Don't notify if already notified as an admin in the step above.
			t.presSingleUserOffline(sess.uid, "acs", params, sess.sid, false)
		}

		// Notify new subscriber of topic's online status
		if !existingSub {
			t.presSingleUserOffline(sess.uid, "on", nilPresParams, "", false)
		}
	}

	return nil
}

// approveSub processes a request to initiate an invite or approve a subscription request from another user:
// Handle these cases:
// A. Sharer or Approver is inviting another user for the first time (no prior subscription)
// B. Sharer or Approver is re-inviting another user (adjusting modeGiven, modeWant is still Unset)
// C. Approver is changing modeGiven for another user, modeWant != Unset
func (t *Topic) approveSub(h *Hub, sess *Session, target types.Uid, set *MsgClientSet) error {
	log.Printf("approveSub, session uid=%s, target uid=%s", sess.uid.String(), target.String())

	now := types.TimeNow()

	// Access mode values as they were before this request was processed.
	oldWant := types.ModeNone
	oldGiven := types.ModeNone

	// Access mode of the person who is executing this approval process
	var hostMode types.AccessMode

	// Check if approver actually has permission to manage sharing
	if userData, ok := t.perUser[sess.uid]; !ok || !(userData.modeGiven & userData.modeWant).IsSharer() {
		sess.queueOut(ErrPermissionDenied(set.Id, t.original(sess.uid), now))
		return errors.New("topic access denied")
	} else {
		hostMode = userData.modeGiven & userData.modeWant
	}

	// Parse the access mode granted
	modeGiven := types.ModeUnset
	if set.Sub.Mode != "" {
		if err := modeGiven.UnmarshalText([]byte(set.Sub.Mode)); err != nil {
			sess.queueOut(ErrMalformed(set.Id, t.original(sess.uid), now))
			return err
		}

		// Make sure the new permissions are reasonable in P2P topics
		if t.cat == types.TopicCat_P2P {
			modeGiven &= types.ModeCP2P
			if modeGiven != types.ModeNone {
				modeGiven |= types.ModeApprove
			}
		}
	}

	// Make sure only the owner & approvers can set non-default access mode
	if modeGiven != types.ModeUnset && !hostMode.IsAdmin() {
		sess.queueOut(ErrPermissionDenied(set.Id, t.original(sess.uid), now))
		return errors.New("sharer cannot set explicit modeGiven")
	}

	// Make sure no one but the owner can do an ownership transfer
	if modeGiven.IsOwner() && t.owner != sess.uid {
		sess.queueOut(ErrPermissionDenied(set.Id, t.original(sess.uid), now))
		return errors.New("attempt to transfer ownership by non-owner")
	}

	// Check if it's a new invite. If so, save it to database as a subscription.
	// Saved subscription does not mean the user is allowed to post/read
	userData, existingSub := t.perUser[target]
	if !existingSub {

		if modeGiven == types.ModeUnset {
			// Request to use default access mode for the new subscriptions.
			// Assuming LevelAuth. Approver should use non-default access if that is not suitable.
			modeGiven = t.accessFor(auth.LevelAuth)
		}

		// Get user's default access mode to be used as modeWant
		var modeWant types.AccessMode
		if user, err := store.Users.Get(target); err != nil {
			sess.queueOut(ErrUnknown(set.Id, t.original(sess.uid), now))
			return err
		} else if user == nil {
			sess.queueOut(ErrUserNotFound(set.Id, t.original(sess.uid), now))
			return errors.New("user not found")
		} else {
			modeWant = user.Access.Auth
		}

		// Add subscription to database
		sub := &types.Subscription{
			User:      target.String(),
			Topic:     t.name,
			ModeWant:  modeWant,
			ModeGiven: modeGiven,
		}

		if err := store.Subs.Create(sub); err != nil {
			sess.queueOut(ErrUnknown(set.Id, t.original(sess.uid), now))
			return err
		}

		userData = perUserData{
			modeGiven: sub.ModeGiven,
			modeWant:  sub.ModeWant,
			private:   nil,
		}
		t.perUser[target] = userData

	} else {
		// Action on an existing subscription (re-invite or confirm/decline request)

		oldGiven = userData.modeGiven
		oldWant = userData.modeWant

		if modeGiven == types.ModeUnset {
			// Request to re-send invite without changing the access mode
			modeGiven = userData.modeGiven
		} else if modeGiven != userData.modeGiven {
			// Changing the previously assigned value
			userData.modeGiven = modeGiven

			// Save changed value to database
			if err := store.Subs.Update(t.name, target,
				map[string]interface{}{"ModeGiven": modeGiven}); err != nil {
				return err
			}

			t.perUser[target] = userData
		}
	}

	// The user does not want to be bothered, no further action is needed
	if !userData.modeWant.IsJoiner() {
		sess.queueOut(ErrPermissionDenied(set.Id, t.original(sess.uid), now))
		return errors.New("user banned the topic")
	}

	// Access mode has changed.
	if oldGiven != userData.modeGiven {
		params := &PresParams{
			actor:  sess.uid.UserId(),
			target: target.UserId(),
			dWant:  oldWant.Delta(userData.modeWant),
			dGiven: oldGiven.Delta(userData.modeGiven)}

		// Inform the target user.
		t.presSingleUserOffline(target, "acs", params, sess.sid, false)

		// Inform topic admins too
		t.presSubsOffline("acs", params, types.ModeCSharer, sess.sid, false)
	}

	if !existingSub && len(t.sessions) > 0 {
		// Notify the new subscriber that the topic is online
		t.presSingleUserOffline(target, "on", nilPresParams, "", false)
	}

	return nil
}

// replyGetDesc is a response to a get.desc request on a topic, sent to just the session as a {meta} packet
func (t *Topic) replyGetDesc(sess *Session, id, tempName string, opts *MsgGetOpts) error {
	now := time.Now().UTC().Round(time.Millisecond)

	// Check if user requested modified data
	ifUpdated := (opts == nil || opts.IfModifiedSince == nil || opts.IfModifiedSince.Before(t.updated))

	desc := &MsgTopicDesc{CreatedAt: &t.created}
	if !t.updated.IsZero() {
		desc.UpdatedAt = &t.updated
	}

	pud, full := t.perUser[sess.uid]
	if t.cat == types.TopicCat_Me {
		full = true
	}

	if ifUpdated {
		if t.public != nil {
			desc.Public = t.public
		} else if full {
			// p2p topic
			desc.Public = pud.public
		}
	}

	// Request may come from a subscriber (full == true) or a stranger.
	// Give subscriber a fuller description than to a stranger
	if full {
		if t.cat == types.TopicCat_P2P {
			// For p2p topics default access mode makes no sense.
			// Don't report it.
		} else if t.cat == types.TopicCat_Me || (pud.modeGiven & pud.modeWant).IsSharer() {
			desc.DefaultAcs = &MsgDefaultAcsMode{
				Auth: t.accessAuth.String(),
				Anon: t.accessAnon.String()}
		}

		if t.cat != types.TopicCat_Me {
			desc.Acs = &MsgAccessMode{
				Want:  pud.modeWant.String(),
				Given: pud.modeGiven.String(),
				Mode:  (pud.modeGiven & pud.modeWant).String()}
		}

		if ifUpdated {
			desc.Private = pud.private
		}

		// Don't report message IDs to users without Read access.
		if (pud.modeGiven & pud.modeWant).IsReader() {
			desc.SeqId = t.lastId
			// Make sure reported values are sane:
			// t.clearId <= pud.clearId <= t.readId <= t.recvId <= t.lastId
			desc.ClearId = max(pud.clearId, t.clearId)
			desc.ReadSeqId = max(pud.readId, desc.ClearId)
			desc.RecvSeqId = max(pud.recvId, pud.readId)
		}

		// When the topic is first created it may have been assigned a temporary name.
		// Report the temporary name here. It could be empty.
		if tempName != "" && tempName != t.original(sess.uid) {
			desc.TempName = tempName
		}
	}

	sess.queueOut(&ServerComMessage{
		Meta: &MsgServerMeta{
			Id:        id,
			Topic:     t.original(sess.uid),
			Desc:      desc,
			Timestamp: &now}})

	return nil
}

// replySetDesc updates topic metadata, saves it to DB,
// replies to the caller as {ctrl} message, generates {pres} update if necessary
func (t *Topic) replySetDesc(sess *Session, set *MsgClientSet) error {
	now := time.Now().UTC().Round(time.Millisecond)

	assignAccess := func(upd map[string]interface{}, mode *MsgDefaultAcsMode) error {
		if auth, anon, err := parseTopicAccess(mode, types.ModeInvalid, types.ModeInvalid); err != nil {
			return err
		} else if auth.IsOwner() || anon.IsOwner() {
			return errors.New("default 'owner' access is not permitted")
		} else {
			access := make(map[string]interface{})
			if auth != types.ModeInvalid {
				if t.cat == types.TopicCat_Me {
					auth &= types.ModeCP2P
					if auth != types.ModeNone {
						// This is the default access mode for P2P topics.
						// It must be either an N or must include an A permission
						auth |= types.ModeApprove
					}
				}
				access["Auth"] = auth
			}
			if anon != types.ModeInvalid {
				if t.cat == types.TopicCat_Me {
					anon &= types.ModeCP2P
					if anon != types.ModeNone {
						anon |= types.ModeApprove
					}
				}
				access["Anon"] = anon
			}
			if len(access) > 0 {
				upd["Access"] = access
			}
		}
		return nil
	}

	assignGenericValues := func(upd map[string]interface{}, what string, p interface{}) (changed bool) {
		if isNullValue(p) {
			// Request to clear the value
			if upd[what] != nil {
				upd[what] = nil
				changed = true
			}
		} else if p != nil {
			// A new non-nil value
			upd[what] = p
			changed = true
		}
		return
	}

	updateCached := func(upd map[string]interface{}) {
		if tmp, ok := upd["Access"]; ok {
			access := tmp.(map[string]interface{})
			if auth, ok := access["Auth"]; ok {
				if auth != types.ModeInvalid {
					t.accessAuth = auth.(types.AccessMode)
				}
			}
			if anon, ok := access["Anon"]; ok {
				if anon != types.ModeInvalid {
					t.accessAnon = anon.(types.AccessMode)
				}
			}
		}
		if public, ok := upd["Public"]; ok {
			t.public = public
		}
	}

	var err error
	var sendPres bool

	user := make(map[string]interface{})
	topic := make(map[string]interface{})
	sub := make(map[string]interface{})
	if set.Desc != nil {
		if t.cat == types.TopicCat_Me {
			// Update current user
			if set.Desc.DefaultAcs != nil {
				err = assignAccess(user, set.Desc.DefaultAcs)
			}
			if set.Desc.Public != nil {
				sendPres = assignGenericValues(user, "Public", set.Desc.Public)
			}
		} else if t.cat == types.TopicCat_Fnd {
			// User's own tags are sent as fnd.public. Assign them to user.Tags
			if set.Desc.Public != nil {
				if src, ok := set.Desc.Public.([]string); ok && len(src) > 0 {
					tags := make([]string, 0, len(src))
					if filterTags(&tags, src) > 0 {
						// No need to send presence update
						assignGenericValues(user, "Tags", tags)
						t.public = tags
					}
				}
			}
		} else if t.cat == types.TopicCat_P2P {
			// Reject direct changes to P2P topics.
			sess.queueOut(ErrPermissionDenied(set.Id, set.Topic, now))
			return errors.New("attempt to change metadata of a p2p topic")
		} else {
			// Update group topic
			if set.Desc.DefaultAcs != nil || set.Desc.Public != nil {
				if t.owner == sess.uid {
					if set.Desc.DefaultAcs != nil {
						err = assignAccess(topic, set.Desc.DefaultAcs)
					}
					if set.Desc.Public != nil {
						sendPres = assignGenericValues(topic, "Public", set.Desc.Public)
					}
				} else {
					// This is a request from non-owner
					sess.queueOut(ErrPermissionDenied(set.Id, set.Topic, now))
					return errors.New("attempt to change public or permissions by non-owner")
				}
			}
		}

		if err != nil {
			sess.queueOut(ErrMalformed(set.Id, set.Topic, now))
			return err
		}

		if set.Desc.Private != nil {
			assignGenericValues(sub, "Private", set.Desc.Private)
		}
	}

	var change int
	if len(user) > 0 {
		err = store.Users.Update(sess.uid, user)
		change++
	}
	if err == nil && len(topic) > 0 {
		err = store.Topics.Update(t.name, topic)
		change++
	}
	if err == nil && len(sub) > 0 {
		err = store.Subs.Update(t.name, sess.uid, sub)
		change++
	}

	if err != nil {
		sess.queueOut(ErrUnknown(set.Id, set.Topic, now))
		return err
	} else if change == 0 {
		sess.queueOut(InfoNotModified(set.Id, set.Topic, now))
		return errors.New("{set} generated no update to DB")
	}

	// Update values cached in the topic object
	if private, ok := sub["Private"]; ok {
		pud := t.perUser[sess.uid]
		pud.private = private
		t.perUser[sess.uid] = pud
	}
	if t.cat == types.TopicCat_Me {
		updateCached(user)
	} else if t.cat == types.TopicCat_Grp {
		updateCached(topic)
	}

	if sendPres {
		// t.Public has changed, make an announcement
		if t.cat == types.TopicCat_Me {
			t.presUsersOfInterest("upd", "")
			t.presSingleUserOffline(sess.uid, "upd", nilPresParams, sess.sid, false)
		} else {
			t.presSubsOffline("upd", nilPresParams, 0, sess.sid, false)
		}
	}

	sess.queueOut(NoErr(set.Id, set.Topic, now))

	return nil
}

// replyGetSub is a response to a get.sub request on a topic - load a list of subscriptions/subscribers,
// send it just to the session as a {meta} packet
// FIXME(gene): reject request if the user does not have the R permission
func (t *Topic) replyGetSub(sess *Session, id string, opts *MsgGetOpts) error {
	now := types.TimeNow()

	var subs []types.Subscription
	var err error
	var isSharer bool

	if t.cat == types.TopicCat_Me {
		// Fetch user's subscriptions, with Topic.Public denormalized into subscription.
		// Include deleted subscriptions too.
		subs, err = store.Users.GetTopicsAny(sess.uid)
		isSharer = true
	} else if t.cat == types.TopicCat_Fnd {
		// Given a query provided in .private, fetch user's contacts
		if query, ok := t.perUser[sess.uid].private.([]interface{}); ok {
			if query != nil && len(query) > 0 {
				subs, err = store.Users.FindSubs(sess.uid, query)
			}
		}
	} else {
		// TODO(gene): don't load subs from DB, use perUserData - it already contains subscriptions.
		subs, err = store.Topics.GetUsersAny(t.name)
		userData := t.perUser[sess.uid]
		isSharer = (userData.modeGiven & userData.modeWant).IsSharer()
	}

	if err != nil {
		sess.queueOut(ErrUnknown(id, t.original(sess.uid), now))
		return err
	}

	var ifModified time.Time
	var limit int
	if opts != nil {
		if opts.IfModifiedSince != nil {
			ifModified = *opts.IfModifiedSince
		}
		limit = opts.Limit
	}

	if limit <= 0 {
		limit = 1024
	}

	meta := &MsgServerMeta{Id: id, Topic: t.original(sess.uid), Timestamp: &now}
	if subs != nil && len(subs) > 0 {
		meta.Sub = make([]MsgTopicSub, 0, len(subs))
		idx := 0
		for _, sub := range subs {
			if idx == limit {
				break
			}

			// Check if the requester has provided a cut off date for ts of pub & priv updates.
			var sendPubPriv bool
			var deleted bool
			var mts MsgTopicSub

			if ifModified.IsZero() {
				// If IfModifiedSince is not set then the user does not care about managing cache. The user
				// only wants active subscriptions. Skip all deleted subscriptions regarless of deletion time.
				if sub.DeletedAt != nil {
					continue
				}

				sendPubPriv = true
			} else {
				// Skip sending deleted subscriptions if they were deleted before the cut off date.
				// If they are freshly deleted send minimum info
				if sub.DeletedAt != nil {
					if !sub.DeletedAt.After(ifModified) {
						continue
					}
					mts.DeletedAt = sub.DeletedAt
					deleted = true
				}
				sendPubPriv = !deleted && sub.UpdatedAt.After(ifModified)
			}

			uid := types.ParseUid(sub.User)
			isReader := sub.ModeGiven.IsReader() && sub.ModeWant.IsReader()
			var clearId int
			if t.cat == types.TopicCat_Me {
				// The subscriptions user does not care about are marked as deleted
				if !sub.ModeWant.IsJoiner() || !sub.ModeGiven.IsJoiner() {
					deleted = true
				}

				// Reporting user's subscriptions to other topics. P2P topic name is the
				// UID of the other user.
				with := sub.GetWith()
				if with != "" {
					mts.Topic = with
					mts.Online = t.perSubs[with].online && !deleted
				} else {
					mts.Topic = sub.Topic
					mts.Online = t.perSubs[sub.Topic].online && !deleted
				}

				if !deleted {
					if isReader {
						mts.SeqId = sub.GetSeqId()
						// Report whatever is the greatest - soft - or hard- deleted id
						clearId = max(sub.GetHardClearId(), sub.ClearId)
						mts.ClearId = clearId
					}

					lastSeen := sub.GetLastSeen()
					if !lastSeen.IsZero() {
						mts.LastSeen = &MsgLastSeenInfo{
							When:      &lastSeen,
							UserAgent: sub.GetUserAgent()}
					}
				}
			} else {
				// Mark subscriptions that the user does not care about as deleted
				if t.cat == types.TopicCat_Grp && !isSharer &&
					(!sub.ModeWant.IsJoiner() || !sub.ModeGiven.IsJoiner()) {
					deleted = true
				}

				// Reporting subscribers to a group or a p2p topic
				mts.User = uid.UserId()
				if !deleted {
					clearId = max(t.clearId, sub.ClearId)
					if uid == sess.uid && isReader {
						// Report deleted messages for own subscriptions only
						mts.ClearId = clearId
					}

					if t.cat == types.TopicCat_Grp {
						pud := t.perUser[uid]
						mts.Online = pud.online > 0
					}
				}
			}

			if !deleted {
				mts.UpdatedAt = &sub.UpdatedAt

				if isReader {
					// Ensure sanity or ReadId and RecvId:
					mts.ReadSeqId = max(clearId, sub.ReadSeqId)
					mts.RecvSeqId = max(clearId, sub.RecvSeqId)
				}

				if t.cat != types.TopicCat_Fnd {
					mts.Acs.Mode = (sub.ModeGiven & sub.ModeWant).String()
					if isSharer {
						mts.Acs.Want = sub.ModeWant.String()
						mts.Acs.Given = sub.ModeGiven.String()
					}
				}

				// Returning public and private only if they have changed since ifModified
				if sendPubPriv {
					mts.Public = sub.GetPublic()
					// Reporting private only if it's user's own supscription or
					// a synthetic 'private' in 'find' topic where it's a list of tags matched on.
					if uid == sess.uid || t.cat == types.TopicCat_Fnd {
						mts.Private = sub.Private
					}
				}
			} else if mts.DeletedAt == nil {
				mts.DeletedAt = &sub.UpdatedAt
			}

			meta.Sub = append(meta.Sub, mts)
			idx++
		}
	}

	sess.queueOut(&ServerComMessage{Meta: meta})

	return nil
}

// replySetSub is a response to new subscription request or an update to a subscription {set.sub}:
// update topic metadata cache, save/update subs, reply to the caller as {ctrl} message, generate an announcement.
func (t *Topic) replySetSub(h *Hub, sess *Session, set *MsgClientSet) error {
	now := types.TimeNow()

	var uid types.Uid
	if uid = types.ParseUserId(set.Sub.User); uid.IsZero() && set.Sub.User != "" {
		// Invalid user ID
		sess.queueOut(ErrMalformed(set.Id, t.original(sess.uid), now))
		return errors.New("invalid user id")
	}

	// if set.User is not set, request is for the current user
	if uid.IsZero() {
		uid = sess.uid
	}

	var err error
	if uid == sess.uid {
		// Request new subscription or modify own subscription
		err = t.requestSub(h, sess, set.Id, set.Sub.Mode, nil)
	} else {
		// Request to approve/change someone's subscription
		err = t.approveSub(h, sess, uid, set)
	}
	if err != nil {
		return err
	}

	resp := NoErr(set.Id, t.original(sess.uid), now)
	// Report resulting access mode.
	pud := t.perUser[uid]
	params := map[string]interface{}{"acs": MsgAccessMode{
		Given: pud.modeGiven.String(),
		Want:  pud.modeWant.String(),
		Mode:  (pud.modeGiven & pud.modeWant).String()}}
	if uid != sess.uid {
		params["user"] = uid.String()
	}
	resp.Ctrl.Params = params
	sess.queueOut(resp)

	return nil
}

// replyGetData is a response to a get.data request - load a list of stored messages, send them to session as {data}
// response goes to a single session rather than all sessions in a topic
func (t *Topic) replyGetData(sess *Session, id string, req *MsgBrowseOpts) error {
	now := time.Now().UTC().Round(time.Millisecond)

	// Check if the user has permission to read the topic
	if userData := t.perUser[sess.uid]; !(userData.modeGiven & userData.modeWant).IsReader() {
		sess.queueOut(NoErr(id, t.original(sess.uid), now))
		return nil
	}

	opts := msgOpts2storeOpts(req, t.perUser[sess.uid].clearId)

	messages, err := store.Messages.GetAll(t.name, sess.uid, opts)
	if err != nil {
		log.Println("topic: error loading topics ", err)
		sess.queueOut(ErrUnknown(id, t.original(sess.uid), now))
		return err
	}

	// Push the list of messages to the client as {data}.
	// Messages are sent in reverse order than fetched from DB to make it easier for
	// clients to process.
	if messages != nil {
		for i := len(messages) - 1; i >= 0; i-- {
			mm := messages[i]

			from := types.ParseUid(mm.From)
			msg := &ServerComMessage{Data: &MsgServerData{
				Topic:     t.original(sess.uid),
				Head:      mm.Head,
				SeqId:     mm.SeqId,
				From:      from.UserId(),
				Timestamp: mm.CreatedAt,
				Content:   mm.Content}}

			// Clear content if the message was soft-deleted for the current user
			if mm.DeletedAt != nil {
				msg.Data.Head = nil
				msg.Data.Content = nil
				msg.Data.DeletedAt = mm.DeletedAt
			}

			sess.queueOut(msg)

		}
	}
	// Inform the requester that all the data has been served.
	sess.queueOut(NoErr(id, t.original(sess.uid), now))

	return nil
}

// replyDelMsg deletes (soft or hard) messages in response to del.msg packet.
func (t *Topic) replyDelMsg(sess *Session, del *MsgClientDel) error {
	now := time.Now().UTC().Round(time.Millisecond)

	var err error
	var filteredList []int
	if del.Before > t.lastId || del.Before < 0 {
		err = errors.New("del.msg: invalid parameter 'before'")
	} else if del.Before == 0 {
		if del.SeqList == nil || len(del.SeqList) == 0 {
			err = errors.New("del.msg without parameters")
		} else {
			for _, seq := range del.SeqList {
				if seq > t.lastId && seq < 0 {
					err = errors.New("del.msg: invalid entry in list")
					break
				}
				if seq == 0 {
					continue
				}

				filteredList = append(filteredList, seq)
				if len(filteredList) == MAX_SEQ_COUNT {
					break
				}
			}

			if len(filteredList) == 0 {
				err = errors.New("del.msg: no valid entries in list")
			}
		}
	}

	if err != nil {
		sess.queueOut(ErrMalformed(del.Id, t.original(sess.uid), now))
		return err
	}

	pud := t.perUser[sess.uid]
	if !(pud.modeGiven & pud.modeWant).IsDeleter() {
		// User must have an R permission: if the user cannot read messages, he has
		// no business of deleting them.
		if !(pud.modeGiven & pud.modeWant).IsReader() {
			sess.queueOut(ErrPermissionDenied(del.Id, t.original(sess.uid), now))
			return errors.New("del.msg: permission denied")
		}

		// User has just the R permission, cannot hard-delete messages, silently
		// switching to soft-deleting
		del.Hard = false
	}

	if del.Before > 0 {
		// Make sure user has not deleted the messages already
		if (del.Before <= t.clearId) || (!del.Hard && del.Before <= pud.clearId) {
			sess.queueOut(InfoNoAction(del.Id, t.original(sess.uid), now))
			return nil
		}

		err = store.Messages.Delete(t.name, sess.uid, del.Hard, del.Before)
	} else {
		// del.List != nil

		err = store.Messages.DeleteList(t.name, sess.uid, del.Hard, filteredList)
	}

	if err != nil {
		sess.queueOut(ErrUnknown(del.Id, t.original(sess.uid), now))
		return err
	}

	var params *PresParams
	if del.Before > 0 {
		if del.Hard {
			t.clearId = del.Before
			params = &PresParams{seqId: del.Before, actor: sess.uid.UserId()}
		} else {
			pud.clearId = del.Before
			if pud.readId < pud.clearId {
				pud.readId = pud.clearId
			}
			if pud.recvId < pud.readId {
				pud.recvId = pud.readId
			}
			t.perUser[sess.uid] = pud
		}
	} else if del.Hard {
		params = &PresParams{seqList: filteredList, actor: sess.uid.UserId()}
	}

	if del.Hard {
		// Broadcast the change to all, online and offline, exclude the session making the change.
		t.presSubsOnline("del", "", params, types.ModeRead, sess.sid)
		t.presSubsOffline("del", params, types.ModeRead, sess.sid, true)
	} else {
		// Notify user's other sessions
		t.presPubMessageCount(sess.uid, filteredList, del.Before, 0, 0, sess.sid)
	}

	sess.queueOut(NoErr(del.Id, t.original(sess.uid), now))

	return nil
}

// Shut down the topic in response to {del what="topic"} request
// See detailed description at hub.topicUnreg()
// 1. Checks if the requester is the owner. If so:
// 1.2 Evict all sessions
// 1.3 Ask hub to unregister self
// 1.4 Exit the run() loop
// 2. If requester is not the owner:
// 2.1 If this is a p2p topic:
// 2.1.1 Check if the other subscription still exists, if so, treat request as {leave unreg=true}
// 2.1.2 If the other subscription does not exist, delete topic
// 2.2 If this is not a p2p topic, treat it as {leave unreg=true}
func (t *Topic) replyDelTopic(h *Hub, sess *Session, del *MsgClientDel) error {
	if t.owner != sess.uid {
		// Cases 2.1.1 and 2.2
		if t.cat != types.TopicCat_P2P || len(t.perUser) > 1 {
			return t.replyLeaveUnsub(h, sess, del.Id)
		}
	}

	// Notifications are sent from the topic loop.

	for s, _ := range t.sessions {
		delete(t.sessions, s)
		s.detach <- t.name
	}

	return nil
}

func (t *Topic) replyDelSub(h *Hub, sess *Session, del *MsgClientDel) error {
	now := types.TimeNow()

	var err error

	// Get ID of the affected user
	uid := types.ParseUserId(del.User)

	pud := t.perUser[sess.uid]
	if !(pud.modeGiven & pud.modeWant).IsAdmin() {
		err = errors.New("del.sub: permission denied")
	} else if uid.IsZero() || uid == sess.uid {
		// Cannot delete self-subscription. User [leave unsub] or [delete topic]
		err = errors.New("del.sub: cannot delete self-subscription")
	} else if t.cat == types.TopicCat_P2P {
		// Don't try to delete the other P2P user
		err = errors.New("del.sub: cannot apply to a P2P topic")
	}

	if err != nil {
		sess.queueOut(ErrPermissionDenied(del.Id, t.original(sess.uid), now))
		return err
	}

	pud, ok := t.perUser[uid]
	if !ok {
		sess.queueOut(InfoNoAction(del.Id, t.original(sess.uid), now))
		return errors.New("del.sub: user not found")
	}

	// Check if the user being ejected is the owner.
	if (pud.modeGiven & pud.modeWant).IsOwner() {
		err = errors.New("del.sub: cannot evict topic owner")
	} else if !pud.modeWant.IsJoiner() {
		// If the user has banned the topic, subscription should not be deleted. Otherwise user may be re-invited
		// which defeats the purpose of banning.
		err = errors.New("del.sub: cannot delete banned subscription")
	}

	if err != nil {
		sess.queueOut(ErrPermissionDenied(del.Id, t.original(sess.uid), now))
		return err
	}

	// Delete user's subscription from the database
	if err := store.Subs.Delete(t.name, uid); err != nil {
		sess.queueOut(ErrUnknown(del.Id, t.original(sess.uid), now))

		return err
	}

	sess.queueOut(NoErr(del.Id, t.original(sess.uid), now))

	t.evictUser(uid, true, "")

	return nil
}

func (t *Topic) replyLeaveUnsub(h *Hub, sess *Session, id string) error {
	now := types.TimeNow()

	if t.owner == sess.uid {
		if id != "" {
			sess.queueOut(ErrPermissionDenied(id, t.original(sess.uid), now))
		}
		return errors.New("replyLeaveUnsub: owner cannot unsubscribe")
	}

	// Delete user's subscription from the database
	if err := store.Subs.Delete(t.name, sess.uid); err != nil {
		if id != "" {
			sess.queueOut(ErrUnknown(id, t.original(sess.uid), now))
		}

		return err
	}

	if id != "" {
		sess.queueOut(NoErr(id, t.original(sess.uid), now))
	}

	// Evict all user's sessions and clear cached data
	t.evictUser(sess.uid, true, sess.sid)

	return nil
}

// evictUser evicts given user's sessions from the topic and clears user's cached data, if requested
func (t *Topic) evictUser(uid types.Uid, unsub bool, skip string) {
	now := types.TimeNow()

	pud := t.perUser[uid]

	// First notify topic subscribers that the user has left the topic
	if t.cat == types.TopicCat_Grp {
		if unsub {
			// Let admins know
			t.presSubsOnline("acs", uid.UserId(),
				&PresParams{
					actor:  skip,
					target: uid.UserId(),
					dWant:  pud.modeWant.Delta(types.ModeNone),
					dGiven: pud.modeGiven.Delta(types.ModeNone)},
				types.ModeCAdmin, skip)
			// Let affected user know
			t.presSingleUserOffline(uid, "gone", nilPresParams, "", false)
		} else {
			// Let all 'R' users know
			t.presSubsOnline("off", uid.UserId(), nilPresParams, types.ModeRead, skip)
		}
	} else if t.cat == types.TopicCat_P2P && unsub {
		// Notify user's own sessions.
		t.presSingleUserOffline(uid, "gone", nilPresParams, "", false)
		// TODO: send notification to user1's 'me' to remove user2 from perSubs and
		// send an "off" notification to user2
	}

	// Save topic name. It won't be available later
	original := t.original(uid)

	// Second - detach user from topic
	if unsub {
		// Delete per-user data
		delete(t.perUser, uid)
	} else {
		// Clear online status
		pud.online = 0
		t.perUser[uid] = pud
	}

	// Detach all user's sessions
	for sess, _ := range t.sessions {
		if sess.uid == uid {
			delete(t.sessions, sess)
			sess.detach <- t.name
			if sess.sid != skip {
				sess.queueOut(NoErrEvicted("", original, now))
			}
		}
	}
}

// Prepares a payload to be delivered to a mobile device as a push notification.
func (t *Topic) makePushReceipt(data *MsgServerData) *pushReceipt {
	idx := make(map[types.Uid]int, len(t.perUser))
	receipt := push.Receipt{
		To: make([]push.PushTo, len(t.perUser)),
		Payload: push.Payload{
			Topic:     data.Topic,
			From:      data.From,
			Timestamp: data.Timestamp,
			SeqId:     data.SeqId,
			Content:   data.Content}}

	i := 0
	for uid, pud := range t.perUser {
		if (pud.modeWant & pud.modeGiven).IsPresencer() {
			// Only send to those users who have notifications enabled
			receipt.To[i].User = uid
			idx[uid] = i
			i++
		}
	}

	return &pushReceipt{rcpt: &receipt, uidMap: idx}
}

func (t *Topic) mostRecentSession() *Session {
	var sess *Session
	var latest time.Time
	for s, _ := range t.sessions {
		if s.lastAction.After(latest) {
			sess = s
			latest = s.lastAction
		}
	}
	return sess
}

func (t *Topic) suspend() {
	atomic.StoreInt32((*int32)(&t.suspended), 1)
}

func (t *Topic) resume() {
	atomic.StoreInt32((*int32)(&t.suspended), 0)
}

func (t *Topic) isSuspended() bool {
	return atomic.LoadInt32((*int32)(&t.suspended)) != 0
}

// Get topic name suitable for the given client
func (t *Topic) original(uid types.Uid) string {
	if t.cat != types.TopicCat_P2P {
		return t.x_original
	}

	if pud, ok := t.perUser[uid]; ok {
		return pud.topicName
	}

	panic("Invalid P2P topic")
}

// Get topic name suitable for the given client
func (t *Topic) p2pOtherUser(uid types.Uid) types.Uid {
	if t.cat == types.TopicCat_P2P {
		for u2 := range t.perUser {
			if u2.Compare(uid) != 0 {
				return u2
			}
		}
		panic("Invalid P2P topic")
	}
	panic("Not P2P topic")
}

func (t *Topic) accessFor(authLvl int) types.AccessMode {
	return selectAccessMode(authLvl, t.accessAnon, t.accessAuth, getDefaultAccess(t.cat, true))
}

// Helper function to select access mode for the given auth level
func selectAccessMode(authLvl int, anonMode, authLMode, rootMode types.AccessMode) types.AccessMode {
	switch authLvl {
	case auth.LevelNone:
		return types.ModeNone
	case auth.LevelAnon:
		return anonMode
	case auth.LevelAuth:
		return authLMode
	case auth.LevelRoot:
		return rootMode
	default:
		return types.ModeNone
	}
}

// Get default modeWant for the given topic category
func getDefaultAccess(cat types.TopicCat, auth bool) types.AccessMode {
	if !auth {
		return types.ModeNone
	}

	switch cat {
	case types.TopicCat_P2P:
		return types.ModeCP2P
	case types.TopicCat_Fnd:
		return types.ModeNone
	case types.TopicCat_Grp:
		return types.ModeCPublic
	case types.TopicCat_Me:
		return types.ModeCSelf
	default:
		panic("Unknown topic category")
	}
}

// Takes get.data parameters and ClearID, returns database query parameters
func msgOpts2storeOpts(req *MsgBrowseOpts, clearId int) *types.BrowseOpt {
	var opts *types.BrowseOpt
	if req != nil || clearId > 0 {
		opts = &types.BrowseOpt{}
		if req != nil {
			opts.Limit = req.Limit
			if req.SinceId != 0 || req.BeforeId != 0 {
				opts.Since = req.SinceId
				opts.Before = req.BeforeId
			} else if req.SinceTs != nil || req.BeforeTs != nil {
				opts.ByTime = true
				opts.After = req.SinceTs
				opts.Until = req.BeforeTs
			}
		}
		if clearId > opts.Since {
			// ClearId deletes mesages upto and including the value itself. Since shows message starting
			// with the value itself, thus must add 1 to make sure the last deleted message is not shown.
			opts.Since = clearId + 1
		}
	}
	return opts
}

func isNullValue(i interface{}) bool {
	// Del control character
	const CLEAR_VALUE = "\u2421"
	if str, ok := i.(string); ok {
		return str == CLEAR_VALUE
	}
	return false
}

func topicCat(name string) types.TopicCat {
	return types.GetTopicCat(name)
}

// Generate random string as a name of the group topic
func genTopicName() string {
	return "grp" + store.GetUidString()
}
