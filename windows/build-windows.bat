@echo off
setlocal enabledelayedexpansion

echo Building S-UI for Windows...

cd /d "%~dp0.."

where sh >nul 2>&1
if errorlevel 1 (
    echo Error: Git Bash ^(sh^) is required to apply the fork's uTLS patch.
    exit /b 1
)

REM Check if Go is installed
go version >nul 2>&1
if errorlevel 1 (
    echo Error: Go is not installed or not in PATH
    echo Please install Go from https://golang.org/dl/
    pause
    exit /b 1
)

REM Check if Node.js is installed
node --version >nul 2>&1
if errorlevel 1 (
    echo Error: Node.js is not installed or not in PATH
    echo Please install Node.js from https://nodejs.org/
    pause
    exit /b 1
)

echo Building frontend...
cd frontend
call npm install
if errorlevel 1 (
    echo Error: Failed to install frontend dependencies
    pause
    exit /b 1
)

call npm run build
if errorlevel 1 (
    echo Error: Failed to build frontend
    pause
    exit /b 1
)

cd ..

echo Creating web/html directory...
if not exist "web\html" mkdir "web\html"

echo Copying frontend build files...
xcopy "frontend\dist\*" "web\html\" /E /Y /Q

echo Building backend...
set CGO_ENABLED=1
set GOOS=windows
set GOARCH=amd64

REM Try to build with CGO first
sh -c ". ./build-tags.sh; sh scripts/relay-go.sh build -ldflags \"$(ldflags_for windows)\" -tags \"$(tags_for windows)\" -o sui.exe main.go"
if errorlevel 1 (
    echo Warning: CGO build failed, trying without CGO...
    set CGO_ENABLED=0
    sh -c ". ./build-tags.sh; sh scripts/relay-go.sh build -ldflags \"$(ldflags_for windows)\" -tags \"$(tags_for windows)\" -o sui.exe main.go"
    if errorlevel 1 (
        echo Error: Failed to build backend
        pause
        exit /b 1
    )
    echo Built without CGO (some features may be limited)
) else (
    echo Built with CGO
)

echo Build completed successfully!
echo Output: sui.exe
pause
