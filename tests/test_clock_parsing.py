"""Unit tests for broadcast clock parsing."""
import pytest
import sys
import os

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from src.activities.download import parse_broadcast_clock, validate_timestamp


class TestParseBroadcastClock:
    """Test cases for parse_broadcast_clock() function."""
    
    # Standard time formats (no period indicator)
    def test_standard_first_half(self):
        assert parse_broadcast_clock("34:12") == 34
        
    def test_standard_second_half(self):
        assert parse_broadcast_clock("84:28") == 84
        
    def test_standard_extra_time(self):
        assert parse_broadcast_clock("112:54") == 112
    
    # Half indicators - relative times
    def test_2h_relative(self):
        assert parse_broadcast_clock("2H 5:00") == 50  # 45 + 5
        
    def test_2h_relative_15min(self):
        assert parse_broadcast_clock("2H 15:30") == 60  # 45 + 15
        
    def test_1h_explicit(self):
        assert parse_broadcast_clock("1H 35:00") == 35
    
    # Half indicators - absolute times (shouldn't add offset)
    def test_2h_absolute(self):
        assert parse_broadcast_clock("2H 67:00") == 67  # Already >= 45, don't add
        
    def test_2h_exactly_45(self):
        assert parse_broadcast_clock("2H 45:00") == 45  # Exactly 45, don't add
    
    # Extra time indicators - relative times
    def test_et_relative_4min(self):
        assert parse_broadcast_clock("ET 04:04") == 94  # 90 + 4
        
    def test_aet_relative(self):
        assert parse_broadcast_clock("AET 04:04") == 94  # 90 + 4
        
    def test_et_relative_15min(self):
        assert parse_broadcast_clock("ET 15:00") == 105  # 90 + 15
        
    def test_et_with_stoppage_indicator(self):
        # "ET 15:00 +2:43" - the +2:43 is seconds of stoppage, not minutes
        assert parse_broadcast_clock("ET 15:00 +2:43") == 105  # 90 + 15
    
    # Extra time indicators - absolute times (shouldn't add offset)
    def test_et_absolute_102(self):
        assert parse_broadcast_clock("ET 102:53") == 102  # Already >= 90, don't add
        
    def test_et_absolute_95(self):
        assert parse_broadcast_clock("ET 95:00") == 95  # Already >= 90, don't add
        
    def test_et_exactly_90(self):
        assert parse_broadcast_clock("ET 90:00") == 90  # Exactly 90, don't add
    
    # Stoppage time format (explicit base + added)
    def test_stoppage_first_half(self):
        assert parse_broadcast_clock("45+2:30") == 47  # 45 + 2
        
    def test_stoppage_first_half_no_seconds(self):
        assert parse_broadcast_clock("45+2") == 47  # 45 + 2
        
    def test_stoppage_second_half(self):
        assert parse_broadcast_clock("90+3:15") == 93  # 90 + 3
        
    def test_stoppage_second_half_no_seconds(self):
        assert parse_broadcast_clock("90+3") == 93  # 90 + 3
        
    def test_stoppage_extra_time(self):
        assert parse_broadcast_clock("105+2") == 107  # 105 + 2
    
    # Edge cases - unparseable
    def test_none_input(self):
        assert parse_broadcast_clock(None) is None
        
    def test_empty_string(self):
        assert parse_broadcast_clock("") is None
        
    def test_half_time(self):
        assert parse_broadcast_clock("HT") is None
        
    def test_full_time(self):
        assert parse_broadcast_clock("FT") is None
        
    def test_none_string(self):
        assert parse_broadcast_clock("NONE") is None
        
    def test_half_time_full(self):
        assert parse_broadcast_clock("HALF TIME") is None
        
    def test_full_time_full(self):
        assert parse_broadcast_clock("FULL TIME") is None
    
    # Case insensitivity
    def test_case_insensitive_et(self):
        assert parse_broadcast_clock("et 04:04") == 94
        
    def test_case_insensitive_2h(self):
        assert parse_broadcast_clock("2h 5:00") == 50
    
    # Whitespace handling
    def test_extra_whitespace(self):
        assert parse_broadcast_clock("  ET  04:04  ") == 94
        
    def test_2nd_half(self):
        assert parse_broadcast_clock("2ND HALF 5:00") == 50
    
    # Format D: Broadcast stoppage display (frozen base + allocated + sub-clock)
    def test_broadcast_stoppage_first_half_early(self):
        # 45:00 +2 00:43 → sub-clock has 0 full minutes elapsed → 45
        assert parse_broadcast_clock("45:00 +2 00:43") == 45
    
    def test_broadcast_stoppage_second_half_2min(self):
        # 90:00 +4 02:17 → sub-clock has 2 full minutes elapsed → 92
        assert parse_broadcast_clock("90:00 +4 02:17") == 92
    
    def test_broadcast_stoppage_second_half_3min(self):
        # 90:00 +5 03:45 → sub-clock has 3 full minutes elapsed → 93
        assert parse_broadcast_clock("90:00 +5 03:45") == 93
    
    def test_broadcast_stoppage_just_started(self):
        # 45:00 +3 00:00 → sub-clock at zero → 45
        assert parse_broadcast_clock("45:00 +3 00:00") == 45
    
    def test_broadcast_stoppage_1min_in(self):
        # 90:00 +6 01:30 → sub-clock has 1 full minute → 91
        assert parse_broadcast_clock("90:00 +6 01:30") == 91
    
    def test_broadcast_stoppage_extra_time(self):
        # 120:00 +3 01:15 → sub-clock has 1 full minute → 121
        assert parse_broadcast_clock("120:00 +3 01:15") == 121
    
    # Format E: Base:SS+added (no space, colon in base)
    def test_base_stoppage_with_seconds(self):
        # 90:00+3:15 → 90 + 3 = 93
        assert parse_broadcast_clock("90:00+3:15") == 93
    
    def test_base_stoppage_without_seconds(self):
        # 45:00+2 → 45 + 2 = 47
        assert parse_broadcast_clock("45:00+2") == 47
    
    def test_base_stoppage_second_half(self):
        # 90:00+5 → 90 + 5 = 95
        assert parse_broadcast_clock("90:00+5") == 95


class TestValidateTimestamp:
    """Test cases for validate_timestamp() function."""
    
    def test_match_exact(self):
        # API says 46th minute goal (elapsed=46), expected clock = 45
        clock_verified, extracted, bucket = validate_timestamp(["45:30"], 46, None)
        assert clock_verified is True
        assert extracted == 45
        assert bucket == "A"
    
    def test_match_within_tolerance(self):
        # API says 46th minute (expected broadcast = 45), clock shows 46 (within ±1 of 45)
        clock_verified, extracted, bucket = validate_timestamp(["46:12"], 46, None)
        assert clock_verified is True
        assert extracted == 46
        assert bucket == "A"
    
    def test_extra_time_match(self):
        # API says 95th minute (90 + 5 extra), expected = 94
        clock_verified, extracted, bucket = validate_timestamp(["ET 04:04"], 90, 5)
        assert clock_verified is True
        assert extracted == 94  # 90 + 4
        assert bucket == "A"
    
    def test_mismatch_wrong_half(self):
        # API says 30th minute goal, but clock shows 65
        clock_verified, extracted, bucket = validate_timestamp(["65:00"], 30, None)
        assert clock_verified is False
        assert extracted == 65  # Returns closest for logging
        assert bucket == "B"
    
    def test_no_clock_visible(self):
        # No clock in any frame
        clock_verified, extracted, bucket = validate_timestamp([None, None], 30, None)
        assert clock_verified is False
        assert extracted is None
        assert bucket == "C"
    
    def test_one_frame_matches(self):
        # Only one frame has clock, but it matches
        clock_verified, extracted, bucket = validate_timestamp([None, "29:45"], 30, None)
        assert clock_verified is True
        assert extracted == 29
        assert bucket == "A"
    
    def test_multiple_clocks_one_matches(self):
        # Two frames, one wrong, one right - should pass
        clock_verified, extracted, bucket = validate_timestamp(["65:00", "28:30"], 30, None)
        assert clock_verified is True
        assert extracted == 28
        assert bucket == "A"
    
    def test_half_time_no_clock(self):
        # HT means no usable clock
        clock_verified, extracted, bucket = validate_timestamp(["HT", "HT"], 30, None)
        assert clock_verified is False
        assert extracted is None
        assert bucket == "C"
    
    def test_2h_relative_format(self):
        # API says 50th minute (45 + 5 into 2H), clock shows "2H 5:00"
        clock_verified, extracted, bucket = validate_timestamp(["2H 5:00"], 50, None)
        # Expected = 50 - 1 = 49, clock parses to 50 (45 + 5), diff = 1, within ±1
        assert clock_verified is True
        assert extracted == 50
        assert bucket == "A"
    
    def test_api_elapsed_zero_guard(self):
        # When Temporal replays an in-flight workflow with default event_minute=0,
        # we can't validate — should return bucket C, not bucket B
        clock_verified, extracted, bucket = validate_timestamp(["34:12", "35:30"], 0, None)
        assert clock_verified is False
        assert extracted is None
        assert bucket == "C"
    
    def test_api_elapsed_none_guard(self):
        # Same guard for None
        clock_verified, extracted, bucket = validate_timestamp(["67:00"], 0, 0)
        assert clock_verified is False
        assert extracted is None
        assert bucket == "C"
    
    def test_broadcast_stoppage_match(self):
        # API says 90+3 (expected=92), broadcast shows "90:00 +4 02:17" → 92
        clock_verified, extracted, bucket = validate_timestamp(["90:00 +4 02:17"], 90, 3)
        assert clock_verified is True
        assert extracted == 92
        assert bucket == "A"
    
    def test_broadcast_stoppage_first_half(self):
        # API says 45+1 (expected=45), broadcast shows "45:00 +2 00:43" → 45
        clock_verified, extracted, bucket = validate_timestamp(["45:00 +2 00:43"], 45, 1)
        assert clock_verified is True
        assert extracted == 45
        assert bucket == "A"
    
    # OCR resilience fallback tests
    # Vision models sometimes drop the leading digit (92:36 → 02:36)
    def test_subclock_only_second_half(self):
        # AI misreads "92:36 +8" as "02:36 +8" (drops leading 9)
        # Parser returns 2. API says elapsed=90, extra=3 → expected=92.
        # OCR fallback should try 90 + 2 = 92 → match!
        clock_verified, extracted, bucket = validate_timestamp(["02:36 +8"], 90, 3)
        assert clock_verified is True
        assert extracted == 92  # 90 + 2
        assert bucket == "A"
    
    def test_subclock_only_first_half(self):
        # AI misreads "46:15 +3" as "01:15 +3" (drops leading 4 and 6)
        # Fallback: 45 + 1 = 46 → match!
        clock_verified, extracted, bucket = validate_timestamp(["01:15 +3"], 45, 2)
        assert clock_verified is True
        assert extracted == 46  # 45 + 1
        assert bucket == "A"
    
    def test_subclock_mixed_frames(self):
        # One frame read correctly (92:07), other misread (02:36)
        # Correct read matches directly, misread would need fallback
        clock_verified, extracted, bucket = validate_timestamp(["92:07 +8", "02:36 +8"], 90, 3)
        assert clock_verified is True
        assert extracted == 92  # First match wins (92:07 → 92)
        assert bucket == "A"
    
    def test_subclock_no_fallback_without_extra(self):
        # OCR fallback should NOT trigger when api_extra is None
        # (prevents actual minute-2 goals from getting 90 added)
        clock_verified, extracted, bucket = validate_timestamp(["02:36"], 30, None)
        assert clock_verified is False
        assert extracted == 2
        assert bucket == "B"
    
    def test_subclock_fallback_too_large(self):
        # Parsed minute 20 with api 90+3: corrected=90+20=110, expected=92
        # abs(110-92)=18 → no match. The ±1 comparison naturally rejects this.
        clock_verified, extracted, bucket = validate_timestamp(["20:00"], 90, 3)
        assert clock_verified is False
        assert extracted == 20
        assert bucket == "B"


if __name__ == "__main__":
    pytest.main([__file__, "-v"])
