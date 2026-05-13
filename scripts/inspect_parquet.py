# /// script
# requires-python = ">=3.10"
# dependencies = [
#     "pyarrow",
# ]
# ///

"""
A sample script to inspect one of the output parquet files that make up the iceberg table
"""

from pathlib import Path

from pyarrow.parquet import ParquetFile

def inspect_parquet(path: str) -> None:
    pf = ParquetFile(path)
    md = pf.metadata

    print(f"\nFile: {path}")
    print(f"Rows: {md.num_rows}")
    print(f"Row groups: {md.num_row_groups}")
    print(f"Columns: {md.num_columns}\n")

    for rg_idx in range(md.num_row_groups):
        rg = md.row_group(rg_idx)
        print(f"Row Group {rg_idx}")
        print(f"Total byte size: {rg.total_byte_size}")
        print(f"Num columns: {rg.num_columns}")

        for col_idx in range(rg.num_columns):
            col = rg.column(col_idx)
            stats = col.statistics

            print(f"Column: {col.path_in_schema}")
            print(f"  Physical type: {col.physical_type}")

            if stats is None:
                print("  Statistics: NONE (not written)\n")
                continue

            print(stats)


if __name__ == "__main__":
    PATH = Path("/tmp/iceberg-warehouse/default/triples/data/")
    for path in PATH.iterdir():
        inspect_parquet(path)
        break
