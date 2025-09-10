"""Main entry point for Found Footy"""

import argparse
from found_footy.flows.ingest_flow import ingest_flow  # ✅ UPDATED

def main():
    """Main CLI entry point"""
    parser = argparse.ArgumentParser(description="Found Footy: Fetch football fixtures using Prefect.")
    parser.add_argument(
        "--date",
        type=str,
        help="Date in YYYYMMDD format (default: today)",
        default=None
    )
    args = parser.parse_args()

    print("🚀 Running Found Footy fixtures flow...")
    ingest_flow(date_str=args.date)  # ✅ UPDATED

if __name__ == "__main__":
    main()