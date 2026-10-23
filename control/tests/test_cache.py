import time

from psg_control.cache import TTLCache


def test_ttl_cache_shares_a_value_until_it_expires():
    calls = []
    cache = TTLCache(0.2)
    for _ in range(3):
        assert cache.get("k", lambda: calls.append(1) or len(calls)) == 1
    assert cache.get("other", lambda: "x") == "x"  # keys are apart
    time.sleep(0.25)
    assert cache.get("k", lambda: calls.append(1) or len(calls)) == 2


def test_ttl_zero_never_caches():
    cache = TTLCache(0)
    assert [cache.get("k", lambda n=n: n) for n in range(3)] == [0, 1, 2]
