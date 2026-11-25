"""Convert one file from the command line.

    python xlsform_cli.py import FORM.xlsx OUT_DIR   # writes OUT_DIR/FORM.json and OUT_DIR/FORM.report.txt
    python xlsform_cli.py export FORM.json OUT_DIR   # writes OUT_DIR/FORM.xlsx and OUT_DIR/FORM.report.txt

The review report is printed as well. The exit status is 1 when the file could
not be converted, or when the converted form does not pass the form check.
`make xlsform-import` and `make xlsform-export` run this in the worker image.
"""

import json
import sys
from pathlib import Path

from xlsform import XLSFormError, export_xlsform, import_xlsform, report_text


def main(argv):
    if len(argv) != 4 or argv[1] not in ("import", "export"):
        print(__doc__.strip(), file=sys.stderr)
        return 2
    direction, source, out_dir = argv[1], Path(argv[2]), Path(argv[3])
    out_dir.mkdir(parents=True, exist_ok=True)
    stem = source.name.rsplit(".", 1)[0]
    try:
        if direction == "import":
            result = import_xlsform(source.read_bytes(), source.name)
            report = report_text(result.report)
            if result.problems:
                report += "".join(f"- The converted form does not pass the form check: {p}\n" for p in result.problems)
            else:
                (out_dir / f"{stem}.json").write_text(json.dumps(result.form, ensure_ascii=False, indent=2) + "\n")
            written = f"{stem}.json"
        else:
            data, notes = export_xlsform(json.loads(source.read_text()))
            (out_dir / f"{stem}.xlsx").write_bytes(data)
            report = report_text(notes)
            written = f"{stem}.xlsx"
    except (XLSFormError, json.JSONDecodeError, OSError) as exc:
        print(f"{source.name}: {exc}", file=sys.stderr)
        return 1
    (out_dir / f"{stem}.report.txt").write_text(report)
    print(report, end="")
    if direction == "import" and result.problems:
        print(f"{source.name}: the draft was not written, because it does not pass the form check.", file=sys.stderr)
        return 1
    print(f"Wrote {written} and {stem}.report.txt.")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
