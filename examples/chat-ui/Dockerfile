# Chat UI - Static file server for TruvaG3 chat interfaces
# Uses nginx:alpine for minimal footprint (~20MB)
#
# Serves multiple UIs:
#   /                - Cluster dashboard with health status overview
#   /index.html      - Standard chat UI (for travel-chat-agent)
#   /devops.html     - DevOps chat UI (for devops-chat-agent)
#   /hitl.html       - HITL chat UI (for agent-with-human-approval)
#   /welcome.html    - Legacy welcome page with chat scenario selector

FROM nginx:1.25-alpine

# Remove default nginx config and static files
RUN rm -rf /usr/share/nginx/html/* /etc/nginx/conf.d/default.conf

# Copy custom nginx config
COPY nginx.conf /etc/nginx/conf.d/default.conf

# Copy all static HTML files
COPY dashboard.html /usr/share/nginx/html/
COPY welcome.html /usr/share/nginx/html/
COPY index.html /usr/share/nginx/html/
COPY devops.html /usr/share/nginx/html/
COPY hitl.html /usr/share/nginx/html/

# Ensure proper permissions - this fixes the "forbidden" error
RUN chmod -R 755 /usr/share/nginx/html && \
    chown -R nginx:nginx /usr/share/nginx/html

# Expose port 80
EXPOSE 80

# Run nginx in foreground
CMD ["nginx", "-g", "daemon off;"]
