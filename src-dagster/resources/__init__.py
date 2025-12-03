"""Dagster resources - MongoDB, S3, and external services"""
import os

import boto3
from dagster import ConfigurableResource

from src.data.mongo_store import FootyMongoStore


class S3Resource(ConfigurableResource):
    """S3/MinIO resource for video storage"""
    endpoint_url: str
    access_key: str
    secret_key: str
    bucket_name: str
    
    def get_client(self):
        """Get S3/MinIO client"""
        return boto3.client(
            "s3",
            endpoint_url=self.endpoint_url,
            aws_access_key_id=self.access_key,
            aws_secret_access_key=self.secret_key
        )


class TwitterSessionResource(ConfigurableResource):
    """Twitter session service resource"""
    url: str
    
    def get_session_url(self) -> str:
        """Get Twitter session service URL"""
        return self.url


# Resource instances configured from environment variables
mongo_resource = FootyMongoStore(
    connection_url=os.getenv("MONGODB_URI", "mongodb://founduser:footypass@mongo:27017/found_footy?authSource=admin")
)

s3_resource = S3Resource(
    endpoint_url=os.getenv("S3_ENDPOINT_URL", "http://minio:9000"),
    access_key=os.getenv("S3_ACCESS_KEY", "founduser"),
    secret_key=os.getenv("S3_SECRET_KEY", "footypass"),
    bucket_name=os.getenv("S3_BUCKET_NAME", "footy-videos")
)

twitter_session_resource = TwitterSessionResource(
    url=os.getenv("TWITTER_SESSION_URL", "http://twitter-session:8888")
)
