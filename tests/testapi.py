import pytest
from fastapi.testclient import TestClient
import os

from src.main import app

client = TestClient(app)

def test_health_endpoint():
    """Test that the application starts and returns a healthy status."""
    response = client.get("/health")
    assert response.status_code == 200
    data = response.json()
    assert data["status"] == "healthy"
    assert "version" in data
    assert "llm_providers" in data

def test_status_endpoint():
    """Test that the Root Agent detailed status returns the expected schema."""
    response = client.get("/status")
    assert response.status_code == 200
    data = response.json()
    assert "kernel" in data
    assert data["kernel"]["name"] == "Tendril"
    assert "inventory" in data
    assert "connectivity" in data
    assert "pulse" in data



def test_credits_endpoint():
    """Test that the credits widget returns HTML safely."""
    response = client.get("/v1/credits")
    assert response.status_code == 200
    assert 'class="credits-label"' in response.text
