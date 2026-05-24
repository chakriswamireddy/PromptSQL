import { redirect } from "next/navigation";

// Root route: redirect to login. The tenant slug is resolved after login.
export default function Home() {
  redirect("/login");
}
