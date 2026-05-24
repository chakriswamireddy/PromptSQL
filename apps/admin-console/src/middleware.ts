import { NextResponse } from "next/server";
import type { NextRequest } from "next/server";

const PUBLIC_PATHS = ["/login", "/api/auth"];

export function middleware(request: NextRequest) {
  const { pathname } = request.nextUrl;

  // Always allow public paths.
  if (PUBLIC_PATHS.some((p) => pathname.startsWith(p))) {
    return NextResponse.next();
  }

  // Check for access token cookie. The actual JWT verification happens server-side
  // in the api-gateway; this is a lightweight presence check only.
  const hasSession = request.cookies.has("access_token") ||
    request.cookies.has("refresh_token");

  if (!hasSession) {
    const url = request.nextUrl.clone();
    url.pathname = "/login";
    url.searchParams.set("returnTo", pathname);
    return NextResponse.redirect(url);
  }

  // Set CSRF double-submit cookie if absent.
  const res = NextResponse.next();
  if (!request.cookies.has("csrf_token")) {
    const token = crypto.randomUUID();
    res.cookies.set("csrf_token", token, {
      httpOnly: false, // intentionally readable by JS for double-submit
      sameSite: "strict",
      path: "/",
    });
  }

  return res;
}

export const config = {
  matcher: ["/((?!_next/static|_next/image|favicon.ico).*)"],
};
