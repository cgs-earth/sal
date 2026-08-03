# A SAL module needs nothing beyond a shell to satisfy the SAL Module command
# line interface, so this fixture stays on busybox to keep builds fast.
FROM busybox:1.37

COPY salmodule.sh /usr/local/bin/salmodule

RUN chmod +x /usr/local/bin/salmodule

ENTRYPOINT ["/usr/local/bin/salmodule"]

# Default to the usage message when no subcommand is provided
CMD ["--help"]
