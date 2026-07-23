
FROM python:3.11-slim

# Set the working directory inside the container
WORKDIR /app

# Copy the script and requirements file into the container
COPY sample-sal-module-1.py .
COPY requirements.txt .

# Install the required click package
RUN pip install --no-cache-dir -r requirements.txt 

# Configure the script to be the main executable entrypoint
ENTRYPOINT ["python", "sample-sal-module-1.py"]

# Default to showing the help menu if no subcommands are provided
CMD ["--help"]

