"""Generate tiny ListProduct fixtures using only invented fire observations."""
from pathlib import Path

import h5py
import numpy as np

HERE = Path(__file__).resolve().parent
for minute in ("0000", "0015", "0030", "0045", "0100"):
    at = "20261126" + minute
    with h5py.File(HERE / (at + ".h5"), "w") as h5:
        # The fire cell intersects the fictional demo state's forest and beats.
        fields = {
            "ABS_LINE": ([1291], 1),
            "ABS_PIXEL": ([3123], 1),
            "LATITUDE": ([1643], 100),
            "LONGITUDE": ([8631], 100),
            "FRP": ([420], 10),
            "FIRE_CONFIDENCE": ([78], 100),
            "PIXEL_SIZE": ([1630], 100),
            "BT_MIR": ([3050], 10),
            "ACQTIME": ([10], 1),
        }
        h5.attrs.update({
            "IMAGE_ACQUISITION_TIME": np.bytes_(at + "00"),
            "OVERALL_QUALITY_FLAG": np.bytes_(b"OK"),
            "PROJECTION_NAME": np.bytes_(b"GEOS(+045.5)"),
            "PRODUCT_ALGORITHM_VERSION": np.bytes_(b"2.4.0"),
            "NL": 0 if minute in ("0015", "0100") else 1,
            "COFF": 1857, "LOFF": 1857, "CFAC": 13642337, "LFAC": 13642337,
        })
        if minute not in ("0015", "0100"):
            for name, (values, scale) in fields.items():
                dataset = h5.create_dataset(name, data=np.array(values, dtype="i4" if name == "FRP" else "i2"),
                                            compression="gzip", shuffle=True)
                dataset.attrs.update({"SCALING_FACTOR": float(scale), "OFFSET": 0.0, "MISSING_VALUE": -999})
