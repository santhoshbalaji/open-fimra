"""Serves a built web app on 127.0.0.1 for browser tests.

Usage: python3 -I scripts/serve-static.py DIRECTORY PORT
"""

import functools
import http.server
import sys


class Handler(http.server.SimpleHTTPRequestHandler):
    # Module scripts are refused unless they have a JavaScript type.
    extensions_map = {
        **http.server.SimpleHTTPRequestHandler.extensions_map,
        ".mjs": "text/javascript",
        ".js": "text/javascript",
        ".wasm": "application/wasm",
        ".json": "application/json",
    }

    def end_headers(self):
        self.send_header("Cache-Control", "no-store")
        super().end_headers()


def main():
    directory, port = sys.argv[1], int(sys.argv[2])
    handler = functools.partial(Handler, directory=directory)
    with http.server.ThreadingHTTPServer(("127.0.0.1", port), handler) as server:
        server.serve_forever()


if __name__ == "__main__":
    main()
