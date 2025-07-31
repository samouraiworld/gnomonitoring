import type { Metadata } from "next";
import {
  ClerkProvider,

} from '@clerk/nextjs'
import { Geist, Geist_Mono } from "next/font/google";
import "./globals.css";
import { Navbar } from "./components/Navbar";
import { Footer } from "./components/Footer";

const geistSans = Geist({
  variable: "--font-geist-sans",
  subsets: ["latin"],
});

const geistMono = Geist_Mono({
  variable: "--font-geist-mono",
  subsets: ["latin"],
});

export const metadata: Metadata = {
  title: "Gnomonitoring App",
  description: "Monitoring Gnoland",
};


export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <ClerkProvider>
      <html lang="en">


        <body className="min-h-screen flex flex-col">
          <Navbar />

          <main className="flex-1 container mx-auto px-4 py-6">{children}</main>
          <Footer />
        </body>
      </html>
    </ClerkProvider>
  );
}