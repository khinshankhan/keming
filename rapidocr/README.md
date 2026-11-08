# keming-rapidocr

An optional reader for keming, using PP-OCRv6 small through RapidOCR
(Apache-2.0). It reads screenshots of web pages, chat and documents better
than tesseract, at about 2s a read instead of 0.4s. When it is on `PATH`,
keming uses it instead of tesseract for anything that isn't monospace text.
It reads a PNG on stdin and prints the boxes it finds as JSON.

Setup, in a virtual environment of your choice:

    python3 -m venv ~/.local/share/keming-rapidocr
    ~/.local/share/keming-rapidocr/bin/pip install rapidocr onnxruntime
    printf '#!/bin/sh\nexec ~/.local/share/keming-rapidocr/bin/python -I %s "$@"\n' \
        "$PWD/keming-rapidocr" > ~/.local/bin/keming-rapidocr
    chmod +x ~/.local/bin/keming-rapidocr

RapidOCR downloads its models on the first run; after that it runs offline.
It uses 4 threads and 450 to 750MB of memory while it reads.
