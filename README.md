# keming

> it can tell rn from m

Offline OCR that knows which font you're using, and judges you for it.
Named after the mistake it doesn't make: bad kerning turns "kerning" into
"keming".

    keming [-l lang] [-engine name] [-v] [image]

Reads a screenshot, from a file or as PNG or JPEG on stdin, and prints its
text. It picks the reader per image:

- **cell reader** (`cellread/`): for monospace text such as code. It finds
  the character grid, works out which installed font drew the text, and
  matches every cell against that font's own glyphs, so `l` and `1`, `0` and
  `O`, and indentation come out exact. Pure Go, no model.
- **PP-OCRv6** (`rapid/`, helper in `rapidocr/`): for everything else in
  English, when the optional helper is installed.
- **tesseract** (`tesseract/`): otherwise, and for other languages.

keming loads fonts and models before it reads stdin, so a caller can start it
early and send the image when it has one; frame's `extract` does this while
the area is picked. Empty stdin exits quietly.

Experimental, and private until frame takes it up.
