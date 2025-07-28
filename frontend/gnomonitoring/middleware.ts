import { clerkMiddleware } from "@clerk/nextjs/server";

export default clerkMiddleware();

export const config = {
    matcher: [
        "/configbot(.*)",
        "/webhooks(.*)",
        "/((?!_next|.*\\..*|favicon.ico).*)", // ← pour protéger sign-out etc.
    ],
};
