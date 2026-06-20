package portal

// driftLogo is the brand wordmark in Tinos Bold Italic (the logo's actual
// style), half-block rasterized offline (Pillow + ~/Library/Fonts/
// Tinos-BoldItalic.ttf, size 20) so the CLI carries no font/Python dependency
// at runtime. It renders in the terminal's default foreground, so it inherits
// the brand's dark-logo-on-light / white-logo-on-dark behaviour automatically.
// Regenerate with tools/driftlogo.py.
var driftLogo = []string{
	"       ▀███          ▄██   ▄▄█▀█",
	"        ███           ▀▀   ██▀ ▀  ▄▄",
	"    ▄▄▄▄██▀ ▄▄▄▄ ▄▄ ▄▄▄▄ ▄███▄▄ ▄███▄",
	"  ▄██  ███   ███▀▀█  ██▀  ███   ▄██",
	" ▄██   ███  ███     ███  ▄██    ███",
	" ███  ▄██   ███     ███  ███    ██▀",
	" ▀███▀███▄ ▄██      ██▄  ███    ███▄",
	"                        ▄██",
	"                        ███",
}
