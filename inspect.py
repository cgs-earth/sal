# /// script
# requires-python = ">=3.10"
# dependencies = [
#     "pyarrow",
# ]
# ///

from pathlib import Path
import sys


SCRIPT_DIR = Path(__file__).resolve().parent
ORIGINAL_SYS_PATH = sys.path.copy()
sys.path = [
    path
    for path in sys.path
    if Path(path or ".").resolve() != SCRIPT_DIR
]

try:
    from pyarrow.parquet import ParquetFile
finally:
    sys.path = ORIGINAL_SYS_PATH

PATH = "/tmp/iceberg-warehouse/default/triples/data/00000-0-0b5e67d6-6ca9-4b26-94b5-ec5993096228-00001.parquet"


def inspect_parquet(path: str) -> None:
    pf = ParquetFile(path)
    md = pf.metadata

    print(f"\nFile: {path}")
    print(f"Rows: {md.num_rows}")
    print(f"Row groups: {md.num_row_groups}")
    print(f"Columns: {md.num_columns}\n")

    for rg_idx in range(md.num_row_groups):
        rg = md.row_group(rg_idx)
        print(f"==============================")
        print(f"Row Group {rg_idx}")
        print(f"Total byte size: {rg.total_byte_size}")
        print(f"Num columns: {rg.num_columns}")
        print(f"==============================\n")

        for col_idx in range(rg.num_columns):
            col = rg.column(col_idx)
            stats = col.statistics

            print(f"Column: {col.path_in_schema}")
            print(f"  Physical type: {col.physical_type}")

            if stats is None:
                print("  Statistics: NONE (not written)\n")
                continue

            print("  Statistics: PRESENT")

            try:
                print(f"    null_count: {stats.null_count}")
            except Exception:
                pass

            # String columns: min/max are lexicographic UTF-8
            try:
                print(f"    min: {stats.min}")
            except Exception:
                pass

            try:
                print(f"    max: {stats.max}")
            except Exception:
                pass

            print("")


if __name__ == "__main__":
    inspect_parquet(PATH)
