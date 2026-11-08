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
  `O`, and indentation come out exact. Pure Go, no model. It reads ASCII and
  the Latin letters of western and central Europe, Turkey and Vietnam;
  Chinese, Japanese and Korean, two cells wide in a terminal, it leaves to
  PP-OCRv6 (or tesseract with `-l`) and puts the result back in place.
- **PP-OCRv6** (`rapid/`, helper in `rapidocr/`): for everything else, when
  the optional helper is installed. Its model reads English, Chinese and
  Japanese, and most Latin-script languages.
- **tesseract** (`tesseract/`): otherwise, and for any other language given
  with `-l`. With no `-l`, Korean is read by tesseract's `kor` model when
  PP-OCRv6 finds nothing.

keming aims at English, Chinese and Japanese. When a screenshot in another
language reads wrong, `-l` names it as an ISO 639-1 code, with a script or
region where it matters: `-l fr`, `-l ko`, `-l zh-TW`, `-l sr-Latn`. Without
it, prose in Swedish, Finnish, Dutch, Polish, Romanian, Italian, Portuguese,
Spanish and German read exactly in testing, and code in all of those plus
Danish, French, Czech, Turkish and Vietnamese; prose in those last five,
and Korean inside a terminal line, need `-l`.

keming loads fonts and models before it reads stdin, so a caller can start it
early and send the image when it has one; frame's `extract` does this while
the area is picked. Empty stdin exits quietly.

Experimental, and private until frame takes it up.
