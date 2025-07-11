# Rclone Uploader

A web interface for monitoring and managing rclone uploads with automatic directory watching.

## Features

- Web interface for monitoring upload progress
- Automatic directory watching
- Upload statistics and metrics
- Docker Compose support
- Configurable through environment variables

## Prerequisites

- Docker and Docker Compose
- Rclone configured with your remote(s)

## Quick Start

1. Clone the repository:
   ```bash
   git clone https://github.com/yourusername/rclone_uploader.git
   cd rclone_uploader
   ```

2. Create a .env file:
   ```bash
   # Base directory for qBittorrent downloads
   QBIT_DOWNLOADS_BASE_DIR=/path/to/downloads

   # Rclone remote name (configured in rclone config)
   RCLONE_REMOTE_NAME=gdrive

   # Subdirectories to watch (comma-separated)
   WATCH_SUBDIRS=movies,tv,anime

   # Rclone copy parameters
   COPY_PARAMS=--transfers 4 -P

   # Application settings
   PORT=8050
   TIMEZONE=UTC
   APP_ENV=production
   ALLOWED_ORIGINS=*
   ```

3. Start the application:
   ```bash
   docker-compose up -d
   ```

4. Access the web interface at http://localhost:8050

## Configuration

### Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| QBIT_DOWNLOADS_BASE_DIR | Base directory for downloads | Required |
| RCLONE_REMOTE_NAME | Rclone remote name | gdrive |
| WATCH_SUBDIRS | Subdirectories to watch | movies,tv,anime |
| COPY_PARAMS | Rclone copy parameters | --transfers 4 -P |
| PORT | Web interface port | 8050 |
| TIMEZONE | Container timezone | UTC |
| APP_ENV | Application environment | production |
| ALLOWED_ORIGINS | CORS allowed origins | * |

### Volumes

The Docker Compose configuration mounts several volumes:

- `./db:/app/db` - Persists the SQLite database
- `${QBIT_DOWNLOADS_BASE_DIR}:/downloads:ro` - Mounts the downloads directory as read-only
- `${HOME}/.config/rclone:/root/.config/rclone:ro` - Mounts your rclone configuration

### Logging

Logs are managed using Docker's json-file driver with rotation:
- Maximum size per file: 10MB
- Maximum number of files: 3

## Development

To build and run locally:

```bash
# Build the container
docker-compose build

# Run in development mode
APP_ENV=development docker-compose up
```
---
