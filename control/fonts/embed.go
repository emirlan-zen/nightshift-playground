// Package fonts owns the embedded typefaces used by report rendering.
package fonts

import _ "embed"

//go:embed SpaceGrotesk.ttf
var spaceGrotesk []byte

//go:embed JetBrainsMono-Regular.ttf
var jetBrainsMono []byte

//go:embed JetBrainsMono-Bold.ttf
var jetBrainsMonoBold []byte

func SpaceGrotesk() []byte      { return spaceGrotesk }
func JetBrainsMono() []byte     { return jetBrainsMono }
func JetBrainsMonoBold() []byte { return jetBrainsMonoBold }
