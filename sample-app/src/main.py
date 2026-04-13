import os
import sys

# Read environment variables
app_name = os.environ.get("APP_NAME", "Docksmith Sample")
greeting = os.environ.get("GREETING", "Hello")
version = os.environ.get("APP_VERSION", "1.0.0")

print(f"=== {app_name} v{version} ===")
print(f"{greeting}, World! Running inside a Docksmith container.")
print(f"Python version: {sys.version.split()[0]}")
print(f"Working directory: {os.getcwd()}")
print(f"Filesystem root: isolated from host")

# List /app contents to show the copied files
app_dir = "/app"
if os.path.exists(app_dir):
    print(f"\nFiles in {app_dir}:")
    for f in sorted(os.listdir(app_dir)):
        print(f"  - {f}")

print("\nContainer exiting cleanly.")
# changed
# changed
# changed
