from pydantic_settings import BaseSettings, SettingsConfigDict


class Settings(BaseSettings):
    model_config = SettingsConfigDict(env_file=".env", extra="ignore")

    environment: str = "local"
    version: str = "local"
    otlp_endpoint: str = "localhost:4317"
    unleash_url: str = "http://localhost:4242/api"
    unleash_token: str = "default:development.unleash-insecure-api-token"
    http_port: int = 8083
