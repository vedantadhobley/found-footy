import os
from sqlalchemy import create_engine
from sqlalchemy.orm import sessionmaker

# Use environment variable for database URL, with fallback for development
DATABASE_URL = os.getenv(
    "DATABASE_URL", 
    "postgresql://footy_user:footy_pass@localhost:5433/found_footy"
)

# Create engine and session factory
engine = create_engine(DATABASE_URL)
SessionLocal = sessionmaker(autocommit=False, autoflush=False, bind=engine)