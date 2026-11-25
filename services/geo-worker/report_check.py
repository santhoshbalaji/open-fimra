"""Read situation report PDFs back and check their text.

Usage: python report_check.py DIR

Each NAME.pdf in DIR needs a NAME.json beside it with the page count and the
text the PDF must contain. make situation-report-test-integration writes both.
"""

import json
import sys
from pathlib import Path

from pypdf import PdfReader


def check(pdf):
    expect = json.loads(pdf.with_suffix(".json").read_text(encoding="utf-8"))
    reader = PdfReader(pdf)
    text = " ".join(" ".join(page.extract_text() for page in reader.pages).split())
    problems = []
    if len(reader.pages) != expect["pages"]:
        problems.append(f"has {len(reader.pages)} pages, expected {expect['pages']}")
    problems += [f"does not contain {wanted!r}" for wanted in expect["text"] if wanted not in text]
    return len(reader.pages), problems


def main(folder):
    pdfs = sorted(Path(folder).glob("*.pdf"))
    if not pdfs:
        print(f"No PDF files in {folder}.")
        return 1
    failed = False
    for pdf in pdfs:
        pages, problems = check(pdf)
        if problems:
            failed = True
            for problem in problems:
                print(f"{pdf.name} {problem}")
        else:
            print(f"{pdf.name}: {pages} pages, all expected text found")
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1]))
