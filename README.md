# Project: **rclone_uploader**

A web application that monitors directories for new files, uploads them to remote storage using rclone, and provides a user-friendly web interface for management.  

## Features  

- Monitors specified directories for new files.  
- Uploads new files to remote storage using **rclone**.  
- Displays uploaded files through a web interface.  
- Allows editing environment variables directly via the web interface.  
- Supports real-time updates via WebSocket communication.  

Whenever a file is uploaded to a watch directory or its subdirectories, the application automatically uploads it to the corresponding remote path using rclone. The application also updates the database with file details, ensuring the web interface reflects the latest uploads.  

### Example Workflow  

If your watch directory contains the following path:  
`path/to/watch/directory/book/BookNAME`  

It will be uploaded to the remote as:  
`remote:/book/BookNAME`


This workflow is ideal for setups where multiple services interact, such as:  

- A **youtube-dl** script downloading YouTube videos.  
- Automatically uploading downloaded content to remote storage.  
- Serving content from the remote storage with services like **Jellyfin** or **Plex**.  

For instance, a video downloaded to:  
`path/to/watch/directory/youtube/youtube_video_name`  

Will be uploaded to:  
`remote:/youtube/youtube_video_name`  

Your Jellyfin/Plex setup can then serve content directly from the remote storage.  

---

## Getting Started  

### Prerequisites  

Create a `.env` file in the root directory with the following content:  

```env
PORT=8050
COPY_PARAMS='--progress --ignore-existing --onedrive-chunk-size 120M'
WATCH_SUBDIRS=s,m,seed
BLUEPRINT_DB_URL=./internal/db/database.db
QBIT_DOWNLOADS_BASE_DIR=/path/to/your/downloads
RCLONE_REMOTE_NAME=your_remote_name
RCLONE_CONFIG_PATH=/path/to/your/rclone.conf
APP_ENV=production
WEBSOCKET_URL=ws://localhost:8050/websocket
```  

These variables configure the application for local development. Adjust paths and parameters as needed.  

---

## Makefile Commands  

Use the following commands to build, run, and manage the project:  

### Build & Run  

- **Build with make:**  
  ```bash
  make all
  ```  

- **Build the application:**  
  ```bash
  make build
  ```  

- **Run the application:**  
  ```bash
  make run
  ```  

### Database Management  

- **Create DB container:**  
  ```bash
  make docker-run
  ```  

- **Shutdown DB container:**  
  ```bash
  make docker-down
  ```  

### Development  

- **Live reload the application:**  
  ```bash
  make watch
  ```  

- **Clean up binaries from the last build:**  
  ```bash
  make clean
  ```  

---

## Deployment  

For live deployment, ensure all environment variables in the `.env` file are correctly set up for your production environment. Adjust paths, ports, and rclone configuration as required.  

---
