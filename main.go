// +build !js

package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/deepch/vdk/av"
	"github.com/deepch/vdk/codec/h264parser"
	"github.com/deepch/vdk/format/rtsp"
	websocket "github.com/gorilla/websocket"
	reuseport "github.com/kavu/go_reuseport"
	"github.com/pion/webrtc/v3"
	"github.com/pion/webrtc/v3/pkg/media"
)

func main() {
    port := flag.Int("p", 40046, "port to listen on")
    rtsp_prefix := flag.String("webrtc-prefix", "rtsp://localhost:8554/", "prefix to the rtsp address")
    flag.Parse()
    log.Printf("port %d", *port)
    log.Printf("rtsp prefix '%s'", *rtsp_prefix)
    serve_websocket(*port, *rtsp_prefix)
}

func serve_websocket(port int, webrtc_prefix string) {
     ctx, cancel := context.WithCancel(context.Background())
     mux := http.NewServeMux()
     mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
         start_webrtc(w, r, webrtc_prefix)
     })
     listener, err := reuseport.Listen("tcp", fmt.Sprintf(":%d", port))
     if err != nil {
       panic(err)
     }
     defer listener.Close()
     
     httpServer := &http.Server{
         Addr: fmt.Sprintf(":%d", port),
         Handler: mux,
         BaseContext: func(_ net.Listener) context.Context { return ctx },
    }
    log.Printf("starting server onf port %d", port)
    go func() {
        if err := httpServer.Serve(listener); err != http.ErrServerClosed {
            // it is fine to use Fatal here because it is not main gorutine
            log.Fatalf("HTTP server Serve: %v", err)
        }
    }()

    signalChan := make(chan os.Signal, 1)

    signal.Notify(
        signalChan,
        syscall.SIGHUP,  // kill -SIGHUP XXXX
        syscall.SIGINT,  // kill -SIGINT XXXX or Ctrl+c
        syscall.SIGQUIT, // kill -SIGQUIT XXXX
    )

    <-signalChan
    log.Print("os.Interrupt - shutting down...\n")

    go func() {
        <-signalChan
        log.Fatal("os.Kill - terminating...\n")
    }()

    gracefullCtx, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancelShutdown()

    if err := httpServer.Shutdown(gracefullCtx); err != nil {
        log.Printf("shutdown error: %v\n", err)
        defer os.Exit(1)
        return
    } else {
        log.Printf("gracefully stopped\n")
    }

    // manually cancel context if not using httpServer.RegisterOnShutdown(cancel)
    cancel()

    defer os.Exit(0)
    return
}

func start_webrtc(w http.ResponseWriter, r *http.Request, rtsp_prefix string) {
    log.Printf("starting webrtc...")
    websocket, err := upgrader.Upgrade(w, r, nil)
    if err != nil {
        panic(err)
    }
    defer websocket.Close()
    
    mt, message1, err := websocket.ReadMessage()
    if err != nil {
        panic(err)
    }
    mt, message2, err := websocket.ReadMessage()
    if err != nil {
        panic(err)
    }
    log.Printf("received message")
    source := string(message1)
    log.Printf("source: '" + source + "'")
    offer := webrtc.SessionDescription{}
    Decode(string(message2), &offer)
    log.Printf("offer:\n%s", offer)

    peerConnection, err := webrtc.NewPeerConnection(webrtc.Configuration{
        ICEServers: []webrtc.ICEServer{
            {
                URLs: []string{"stun:stun.l.google.com:19302"},
            },
        },
    })
    if err != nil {
        panic(err)
    }
    peerConnection.OnICEConnectionStateChange(func(connectionState webrtc.ICEConnectionState) {
        fmt.Printf("Connection State has changed %s \n", connectionState.String())
    })
    peerConnection.OnICECandidate(func(candidate webrtc.ICECandidate) {
        fmt.Printf("ice candidate %s \n", candidate.String())
    })

    // Create a video track
    videoTrack, err := webrtc.NewTrackLocalStaticSample(
        webrtc.RTPCodecCapability{
            MimeType: "video/h264",
        },
        "pion-rtsp",
        "pion-rtsp",
    )
    if err != nil {
        panic(err)
    }
    rtpSender, err := peerConnection.AddTrack(videoTrack)
    if err != nil {
        panic(err)
    }

    consume_rtcp(rtpSender)

    local_description := handle_offer(peerConnection, offer)
    log.Printf("local description:\n%s", local_description.SDP)
    if err = websocket.WriteMessage(mt, []byte(Encode(local_description))); err != nil {
        panic(err)
    }
    log.Printf("wrote message")

    run_stream(rtsp_prefix + source, videoTrack)
}

func check_origin(r *http.Request) bool {
    return true;
}

var upgrader = websocket.Upgrader{CheckOrigin:check_origin}

func handle_offer(peer_connection *webrtc.PeerConnection, offer webrtc.SessionDescription) webrtc.SessionDescription {
    if err := peer_connection.SetRemoteDescription(offer); err != nil {
        panic(err)
    }
    answer, err := peer_connection.CreateAnswer(nil)
    if err != nil {
        panic(err)
    }
    gatherComplete := webrtc.GatheringCompletePromise(peer_connection)
    if err = peer_connection.SetLocalDescription(answer); err != nil {
        panic(err)
    }

    // Block until ICE Gathering is complete, disabling trickle ICE
    // we do this because we only can exchange one signaling message
    // in a production application you should exchange ICE Candidates via OnICECandidate
    <-gatherComplete

    return *peer_connection.LocalDescription()    
}

func run_stream(source string, video_track *webrtc.TrackLocalStaticSample) {
    annexbNALUStartCode := func() []byte { return []byte{0x00, 0x00, 0x00, 0x01} }
    session, err := rtsp.Dial(source)
    if err != nil {
        panic(err)
    }
    session.RtpKeepAliveTimeout = 10 * time.Second
    codecs, err := session.Streams()
    if err != nil {
        panic(err)
    }
    for i, t := range codecs {
        log.Println("Stream", i, "is of type", t.Type().String())
    }
    if codecs[0].Type() != av.H264 {
        panic("RTSP feed must begin with a H264 codec")
    }
    if len(codecs) != 1 {
        log.Println("Ignoring all but the first stream.")
    }

    var previousTime time.Duration
    for {
        pkt, err := session.ReadPacket()
        if err != nil {
            break
        }

        if pkt.Idx != 0 {
            //audio or other stream, skip it
            continue
        }

        pkt.Data = pkt.Data[4:]

        // For every key-frame pre-pend the SPS and PPS
        if pkt.IsKeyFrame {
            pkt.Data = append(annexbNALUStartCode(), pkt.Data...)
            pkt.Data = append(codecs[0].(h264parser.CodecData).PPS(), pkt.Data...)
            pkt.Data = append(annexbNALUStartCode(), pkt.Data...)
            pkt.Data = append(codecs[0].(h264parser.CodecData).SPS(), pkt.Data...)
            pkt.Data = append(annexbNALUStartCode(), pkt.Data...)
        }

        bufferDuration := pkt.Time - previousTime
        previousTime = pkt.Time
        if err = video_track.WriteSample(
                media.Sample{Data: pkt.Data, Duration: bufferDuration},
            );
            err != nil && err != io.ErrClosedPipe {
            panic(err)
        }
    }

    if err = session.Close(); err != nil {
        log.Println("session Close error", err)
    }
}

func Encode(obj interface{}) string {
    b, err := json.Marshal(obj)
    if err != nil {
        panic(err)
    }
    return base64.StdEncoding.EncodeToString(b)
}

func Decode(in string, obj interface{}) {
    b, err := base64.StdEncoding.DecodeString(in)
    if err != nil {
        panic(err)
    }
    err = json.Unmarshal(b, obj)
    if err != nil {
        panic(err)
    }
}

func consume_rtcp(rtp_sender *webrtc.RTPSender) {
    // Read incoming RTCP packets
    // Before these packets are retuned they are processed by interceptors. For things
    // like NACK this needs to be called.
    go func() {
        rtcpBuf := make([]byte, 1500)
        for {
            if _, _, rtcpErr := rtp_sender.Read(rtcpBuf); rtcpErr != nil {
                return
            }
        }
    }()    
}
