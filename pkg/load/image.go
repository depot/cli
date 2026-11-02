package load

import (
	"math/rand"
)

// During a download of an image we temporarily store the image with this
// random name to avoid conflicts with any other images.
func RandImageName() string {
	const letterBytes = "abcdefghijklmnopqrstuvwxyz"
	name := make([]byte, 10)
	for i := range name {
		name[i] = letterBytes[rand.Intn(len(letterBytes))]
	}

	return string(name)
}
