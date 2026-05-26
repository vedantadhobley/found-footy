"""Back-compat shim.

Phase 3 (P3c, 2026-05-26) split this file into a package under `src/data/`.
The 29 callsites using `from src.data.mongo_store import get_store` or
`from src.data.mongo_store import FootyMongoStore` continue working via
this shim. New code should import from `src.data.store` directly.
"""

from src.data.store import FootyMongoStore, MongoStoreBase, get_store

__all__ = ["FootyMongoStore", "MongoStoreBase", "get_store"]
