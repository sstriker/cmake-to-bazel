cmake_minimum_required(VERSION 3.20)
project(L39 LANGUAGES C VERSION 0.1.0)

find_package(L19 CONFIG REQUIRED)
find_package(L22 CONFIG REQUIRED)

add_library(L39 STATIC src/L39.c)
target_include_directories(L39 PUBLIC
    $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>
    $<INSTALL_INTERFACE:include>)
target_link_libraries(L39 PUBLIC L19::L19 L22::L22)

install(TARGETS L39
        EXPORT L39Targets
        ARCHIVE DESTINATION lib)
install(EXPORT L39Targets
        FILE L39Targets.cmake
        NAMESPACE L39::
        DESTINATION lib/cmake/L39)
