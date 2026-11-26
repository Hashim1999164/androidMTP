module github.com/Hashim1999164/androidMTP

go 1.27.4

require (
	github.com/hanwen/go-mtpfs v1.0.1
	github.com/hanwen/usb v0.0.0-20141217151552-69aee4530ac7
	github.com/spf13/cobra v1.10.2
	github.com/winfsp/cgofuse v1.6.0
)

require (
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/spf13/pflag v1.0.9 // indirect
)

replace github.com/winfsp/cgofuse => ./third_party/cgofuse
