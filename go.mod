module game-on.eu/webrtc

go 1.16

replace (
	github.com/deepch/vdk => ./vdk
	github.com/pion/webrtc/v3 => ./webrtc
)

require (
	github.com/deepch/vdk v0.0.0-20211113104208-022deeb641f7
	github.com/pion/webrtc/v3 v3.1.10-0.20211119083334-f444ff5b0e5e
	github.com/gorilla/websocket v1.4.2
	github.com/kavu/go_reuseport v1.5.0
)
