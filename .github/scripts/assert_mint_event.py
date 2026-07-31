#!/usr/bin/env python3
"""Assert a /v1/events response contains the E2E mint event (issue #268).

Reads a ListEventsResponse JSON body from stdin and checks for a `mint`
event whose recipient topic matches argv[1] and whose decoded amount is
"4200000" (the fixed amount the CI job mints). Exits 0 and prints the
matching event on success, exits 1 otherwise.
"""
import json
import sys

EXPECTED_AMOUNT = "4200000"


def main() -> int:
    if len(sys.argv) != 2:
        print("usage: assert_mint_event.py <recipient_address>", file=sys.stderr)
        return 2
    recipient = sys.argv[1]

    try:
        data = json.load(sys.stdin)
    except json.JSONDecodeError as e:
        print(f"FAIL: response was not valid JSON: {e}", file=sys.stderr)
        return 1

    for event in data.get("events", []):
        topics = event.get("topics", [])
        if len(topics) >= 3 and topics[0] == "mint" and topics[2] == recipient:
            if event.get("data") != EXPECTED_AMOUNT:
                print(
                    f"FAIL: expected amount {EXPECTED_AMOUNT!r}, got {event.get('data')!r}",
                    file=sys.stderr,
                )
                return 1
            print("Found decoded mint event:", json.dumps(event))
            return 0

    return 1


if __name__ == "__main__":
    sys.exit(main())
