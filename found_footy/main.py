import argparse
from found_footy.flows.fixtures_flow import fixtures_flow

def main():
    parser = argparse.ArgumentParser(description="Found Footy: Fetch football fixtures using Prefect.")
    parser.add_argument(
        "--date",
        type=str,
        help="Date in YYYYMMDD format (default: today)",
        default=None
    )
    args = parser.parse_args()

    print("Running Found Footy fixtures flow...")
    # Run the Prefect flow without scheduling logic
    fixtures_flow(date_str=args.date)

if __name__ == "__main__":
    main()