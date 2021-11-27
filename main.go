package main

import (
	"bytes"
	"flag"
	"fmt"
	"image"
	"image/png"
	"net/url"
	"os"
	"strings"

	"github.com/NekoQ/VideoChat/internal/signal"
	"github.com/gorilla/websocket"
	"github.com/pion/mediadevices"
	"github.com/pion/mediadevices/pkg/codec/opus"
	"github.com/pion/mediadevices/pkg/codec/x264"
	_ "github.com/pion/mediadevices/pkg/driver/camera"     // This is required to register camera adapter
	_ "github.com/pion/mediadevices/pkg/driver/microphone" // This is required to register microphone adapter
	"github.com/pion/mediadevices/pkg/frame"
	"github.com/pion/mediadevices/pkg/prop"
	"github.com/pion/webrtc/v3"
)

func must(err error) {
	if err != nil {
		panic(err)
	}
}

func writeToSocket(conn *websocket.Conn) {
	for msg := range socketMsgs {
		fmt.Printf(string(msg[0]))
		conn.WriteMessage(websocket.TextMessage, msg)
	}
}

var socketMsgs = make(chan []byte, 10)

func main() {

	oneTime := 0

	config := webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{
				URLs: []string{"stun:stun3.l.google.com:19302"},
			},
			{
				URLs:       []string{"turn:fr-turn1.xirsys.com:80?transport=udp"},
				Username:   "lmDp7NjcDMwRkHogcka1NLNLCK9j-_k_hEULF0p9YyZ5TqJP7-LZ5IhHZDq4DESfAAAAAGGdJo9zYWNyZWRuZWtv",
				Credential: "dc243e4e-4c83-11ec-a036-0242ac120004",
			},
		},
	}

	// Create a new RTCPeerConnection
	x264Params, err := x264.NewParams()
	if err != nil {
		panic(err)
	}
	x264Params.BitRate = 500_0000 // 500kbps

	opusParams, err := opus.NewParams()
	if err != nil {
		panic(err)
	}
	codecSelector := mediadevices.NewCodecSelector(
		mediadevices.WithVideoEncoders(&x264Params),
		mediadevices.WithAudioEncoders(&opusParams),
	)

	mediaEngine := webrtc.MediaEngine{}
	codecSelector.Populate(&mediaEngine)
	api := webrtc.NewAPI(webrtc.WithMediaEngine(&mediaEngine))
	peerConnection, err := api.NewPeerConnection(config)
	if err != nil {
		panic(err)
	}

	peerConnection.OnICEConnectionStateChange(func(connectionState webrtc.ICEConnectionState) {
		fmt.Printf("Connection State has changed %s \n", connectionState.String())
	})

	addr := flag.String("addr", "localhost:8080", "http service address")
	url := url.URL{Scheme: "ws", Host: *addr, Path: "/sdp"}
	c, _, err := websocket.DefaultDialer.Dial(url.String(), nil)
	fmt.Println("Connected")
	defer c.Close()
	go writeToSocket(c)

	go func() {
		for {
			_, msg, err := c.ReadMessage()
			must(err)
			smsg := string(msg[:])
			if strings.HasPrefix(smsg, "a") {
				fmt.Println("alice local receviced setting as remote")
				remote := webrtc.SessionDescription{}
				s, err := mediadevices.GetUserMedia(mediadevices.MediaStreamConstraints{
					Video: func(c *mediadevices.MediaTrackConstraints) {
						c.FrameFormat = prop.FrameFormat(frame.FormatI420)
						c.Width = prop.Int(640)
						c.Height = prop.Int(480)
					},
					Audio: func(c *mediadevices.MediaTrackConstraints) {
					},
					Codec: codecSelector,
				})
				if err != nil {
					panic(err)
				}

				for _, track := range s.GetTracks() {
					track.OnEnded(func(err error) {
						fmt.Printf("Track (ID: %s) ended with error: %v\n",
							track.ID(), err)
					})

					_, err = peerConnection.AddTransceiverFromTrack(track,
						webrtc.RtpTransceiverInit{
							Direction: webrtc.RTPTransceiverDirectionSendonly,
						},
					)
					if err != nil {
						panic(err)
					}
				}

				signal.Decode(string(msg[1:]), &remote)
				peerConnection.SetRemoteDescription(remote)
				answer, err := peerConnection.CreateAnswer(nil)
				must(err)
				gatherComplete := webrtc.GatheringCompletePromise(peerConnection)
				err = peerConnection.SetLocalDescription(answer)
				must(err)
				<-gatherComplete
				socketMsgs <- []byte("b" + signal.Encode(answer))
			}
			if strings.HasPrefix(smsg, "b") {
				fmt.Println("bob local received setting as remote")
				remote := webrtc.SessionDescription{}
				signal.Decode(string(msg[1:]), &remote)
				err := peerConnection.SetRemoteDescription(remote)
				must(err)
			}

		}
	}()

	if len(os.Args[1:]) != 0 {
		peerConnection.OnTrack(func(tr *webrtc.TrackRemote, r *webrtc.RTPReceiver) {
			buf := make([]byte, 1264)
			fmt.Println(tr.Kind())
			if tr.Kind() == webrtc.RTPCodecTypeVideo {
				r.Receive(nil)
				_, _, err := r.ReadSimulcast(buf)
				must(err)
				fmt.Println(buf)
				err = os.WriteFile("./test", buf, 0644)
				must(err)
				pack, _, err := tr.ReadRTP()
				fmt.Println(pack)
				must(err)
				img, _, _ := image.Decode(bytes.NewReader(buf))
				out, err := os.Create("./test.png")
				must(err)
				err = png.Encode(out, img)
				must(err)
			}

		})
		peerConnection.OnICECandidate(func(i *webrtc.ICECandidate) {
			if i == nil {
				if oneTime == 0 {
					fmt.Println(oneTime)
					msg := []byte("a" + signal.Encode(*peerConnection.LocalDescription()))
					socketMsgs <- msg
					oneTime += 1
				}
			}
		})
		_, err = peerConnection.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo,
			webrtc.RTPTransceiverInit{
				Direction: webrtc.RTPTransceiverDirectionRecvonly,
			})
		must(err)
		_, err = peerConnection.AddTransceiverFromKind(webrtc.RTPCodecTypeAudio,
			webrtc.RTPTransceiverInit{
				Direction: webrtc.RTPTransceiverDirectionRecvonly,
			})
		must(err)
		sdp, err := peerConnection.CreateOffer(nil)
		must(err)
		peerConnection.SetLocalDescription(sdp)
	}

	select {}
	// a := app.New()
	// w := a.NewWindow("Images")

	// mediaStream, err := mediadevices.GetUserMedia(mediadevices.MediaStreamConstraints{
	// 	Video: func(c *mediadevices.MediaTrackConstraints) {
	// 		c.FrameFormat = prop.FrameFormatOneOf{frame.FormatI420, frame.FormatYUY2}
	// 		c.Width = prop.Int(480)
	// 		c.Height = prop.Int(720)
	// 	},
	// })
	// must(err)
	// videoTrack := mediaStream.GetVideoTracks()[0].(*mediadevices.VideoTrack)
	// defer videoTrack.Close()

	// videoReader := videoTrack.NewReader(false)

	// ticker := time.NewTicker(time.Millisecond)
	// defer ticker.Stop()

	// go func() {
	// 	for range ticker.C {
	// 		frame, release, err := videoReader.Read()
	// 		must(err)
	// 		img := canvas.NewImageFromImage(frame)
	// 		img.SetMinSize(fyne.NewSize(720, 480))

	// 		chatTitle := canvas.NewText("Chat", color.White)
	// 		chatTitle.Alignment = fyne.TextAlignCenter
	// 		msg1 := canvas.NewText("Marius: Message 1", color.White)
	// 		msg2 := canvas.NewText("Test: Message 2", color.White)
	// 		chat := container.New(layout.NewVBoxLayout(), chatTitle, msg1, msg2)

	// 		content := container.New(layout.NewHBoxLayout(), img, layout.NewSpacer(), chat)
	// 		w.SetContent(content)
	// 		w.Resize(fyne.NewSize(720, 480))
	// 		release()
	// 	}
	// }()
	// w.ShowAndRun()
}
func generateImage(filePath string) image.Image {
	f, _ := os.Open(filePath)
	defer f.Close()
	image, _, _ := image.Decode(f)
	return image
}
